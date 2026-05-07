package main

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"nhooyr.io/websocket"
)

type SchemaRegistry struct {
	mu       sync.RWMutex
	dir      string
	packs    map[string]SchemaPack
	types    map[string]protoreflect.MessageDescriptor
	files    *protoregistry.Files
	resolver *dynamicpb.Types
}

type SchemaPack struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Path     string           `json:"path"`
	LoadedAt string           `json:"loaded_at"`
	Types    []TypeDescriptor `json:"types"`
}

type TypeDescriptor struct {
	FullName    string          `json:"full_name"`
	Name        string          `json:"name"`
	Package     string          `json:"package"`
	File        string          `json:"file"`
	PackID      string          `json:"pack_id"`
	Fields      []FieldInfo     `json:"fields"`
	JSONSchema  map[string]any  `json:"json_schema"`
	ExampleJSON json.RawMessage `json:"example_json"`
}

type FieldInfo struct {
	Name        string `json:"name"`
	JSONName    string `json:"json_name"`
	Type        string `json:"type"`
	Repeated    bool   `json:"repeated"`
	Map         bool   `json:"map"`
	Required    bool   `json:"required"`
	MessageType string `json:"message_type,omitempty"`
	EnumType    string `json:"enum_type,omitempty"`
}

type schemaPacksResponse struct {
	Packs []SchemaPack `json:"packs"`
}

type schemaTypesResponse struct {
	Types []TypeDescriptor `json:"types"`
}

func NewSchemaRegistry(dir string) (*SchemaRegistry, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	reg := &SchemaRegistry{
		dir:   dir,
		packs: make(map[string]SchemaPack),
		types: make(map[string]protoreflect.MessageDescriptor),
	}
	reg.resetDescriptorsLocked()
	if err := reg.Load(); err != nil {
		return nil, err
	}
	return reg, nil
}

func (s *SchemaRegistry) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.packs = make(map[string]SchemaPack)
	s.types = make(map[string]protoreflect.MessageDescriptor)
	s.resetDescriptorsLocked()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packPath := filepath.Join(s.dir, entry.Name())
		if _, err := s.registerPackLocked(packPath); err != nil {
			return fmt.Errorf("loading schema pack %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *SchemaRegistry) Packs() []SchemaPack {
	s.mu.RLock()
	defer s.mu.RUnlock()

	packs := make([]SchemaPack, 0, len(s.packs))
	for _, pack := range s.packs {
		packs = append(packs, pack)
	}
	sort.Slice(packs, func(i, j int) bool {
		return packs[i].Name < packs[j].Name
	})
	return packs
}

func (s *SchemaRegistry) Types() []TypeDescriptor {
	s.mu.RLock()
	defer s.mu.RUnlock()

	types := make([]TypeDescriptor, 0)
	for _, pack := range s.packs {
		types = append(types, pack.Types...)
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i].FullName < types[j].FullName
	})
	return types
}

func (s *SchemaRegistry) RegisterPack(path string) (SchemaPack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registerPackLocked(path)
}

func (s *SchemaRegistry) Encode(typeName string, payload json.RawMessage) (EncodedPayload, error) {
	typeName = strings.TrimSpace(typeName)
	if typeName == "" {
		return EncodedPayload{}, fmt.Errorf("type name is required")
	}

	s.mu.RLock()
	desc := s.types[typeName]
	resolver := s.resolver
	s.mu.RUnlock()
	if desc == nil {
		return EncodedPayload{}, fmt.Errorf("schema type %q is not loaded", typeName)
	}

	msg := dynamicpb.NewMessage(desc)
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := (protojson.UnmarshalOptions{Resolver: resolver}).Unmarshal(payload, msg); err != nil {
		return EncodedPayload{}, err
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return EncodedPayload{}, err
	}
	return EncodedPayload{
		Data:        data,
		Kind:        websocket.MessageBinary,
		Value:       base64.StdEncoding.EncodeToString(data),
		ContentType: "application/x-protobuf",
		TypeName:    typeName,
	}, nil
}

func (s *SchemaRegistry) ImportUploadedPack(filename string, reader io.Reader) (SchemaPack, error) {
	name := sanitizePackName(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if name == "" {
		name = fmt.Sprintf("schema-pack-%d", time.Now().Unix())
	}
	packID := uniquePackID(name)
	dest := filepath.Join(s.dir, packID)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return SchemaPack{}, err
	}

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".proto":
		data, err := io.ReadAll(io.LimitReader(reader, 32<<20))
		if err != nil {
			os.RemoveAll(dest)
			return SchemaPack{}, err
		}
		if err := os.WriteFile(filepath.Join(dest, filepath.Base(filename)), data, 0o644); err != nil {
			os.RemoveAll(dest)
			return SchemaPack{}, err
		}
	case ".zip":
		tmp, err := os.CreateTemp("", "ditto-schema-*.zip")
		if err != nil {
			os.RemoveAll(dest)
			return SchemaPack{}, err
		}
		tmpPath := tmp.Name()
		if _, err := io.Copy(tmp, io.LimitReader(reader, 128<<20)); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			os.RemoveAll(dest)
			return SchemaPack{}, err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpPath)
			os.RemoveAll(dest)
			return SchemaPack{}, err
		}
		if err := extractZip(tmpPath, dest); err != nil {
			os.Remove(tmpPath)
			os.RemoveAll(dest)
			return SchemaPack{}, err
		}
		os.Remove(tmpPath)
	default:
		os.RemoveAll(dest)
		return SchemaPack{}, fmt.Errorf("upload a .proto file or a .zip schema pack")
	}

	pack, err := s.RegisterPack(dest)
	if err != nil {
		os.RemoveAll(dest)
		return SchemaPack{}, err
	}
	return pack, nil
}

func (s *SchemaRegistry) registerPackLocked(packPath string) (SchemaPack, error) {
	protos, err := protoFiles(packPath)
	if err != nil {
		return SchemaPack{}, err
	}
	if len(protos) == 0 {
		return SchemaPack{}, fmt.Errorf("no .proto files found")
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: []string{packPath, s.dir},
		}),
	}
	files, err := compiler.Compile(context.Background(), protos...)
	if err != nil {
		return SchemaPack{}, err
	}

	packID := filepath.Base(packPath)
	pack := SchemaPack{
		ID:       packID,
		Name:     packID,
		Path:     packPath,
		LoadedAt: time.Now().Format(time.RFC3339),
		Types:    make([]TypeDescriptor, 0),
	}
	for _, file := range files {
		if err := s.registerFileRecursiveLocked(file); err != nil {
			return SchemaPack{}, err
		}
	}
	s.resolver = dynamicpb.NewTypes(s.files)

	for _, file := range files {
		collectMessageTypes(file.Messages(), file, packID, &pack.Types, s.types)
	}
	sort.Slice(pack.Types, func(i, j int) bool {
		return pack.Types[i].FullName < pack.Types[j].FullName
	})
	s.packs[pack.ID] = pack
	return pack, nil
}

func (s *SchemaRegistry) resetDescriptorsLocked() {
	s.files = &protoregistry.Files{}
	s.resolver = dynamicpb.NewTypes(s.files)
}

func (s *SchemaRegistry) registerFileRecursiveLocked(file protoreflect.FileDescriptor) error {
	if _, err := s.files.FindFileByPath(file.Path()); err == nil {
		return nil
	}
	imports := file.Imports()
	for i := 0; i < imports.Len(); i++ {
		if err := s.registerFileRecursiveLocked(imports.Get(i)); err != nil {
			return err
		}
	}
	return s.files.RegisterFile(file)
}

func RegisterSchemaRoutes(mux *http.ServeMux, schemas *SchemaRegistry) {
	if schemas == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/schemas/packs", func(w http.ResponseWriter, r *http.Request) {
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(schemaPacksResponse{Packs: schemas.Packs()})
		case http.MethodPost:
			if err := r.ParseMultipartForm(128 << 20); err != nil {
				http.Error(w, "invalid multipart upload: "+err.Error(), http.StatusBadRequest)
				return
			}
			file, header, err := r.FormFile("pack")
			if err != nil {
				http.Error(w, "missing upload field \"pack\"", http.StatusBadRequest)
				return
			}
			defer file.Close()

			pack, err := schemas.ImportUploadedPack(header.Filename, file)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(pack)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/__ditto__/api/schemas/types", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(schemaTypesResponse{Types: schemas.Types()})
	})
}

func protoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".proto" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(files)
	return files, err
}

func collectMessageTypes(messages protoreflect.MessageDescriptors, file protoreflect.FileDescriptor, packID string, out *[]TypeDescriptor, index map[string]protoreflect.MessageDescriptor) {
	for i := 0; i < messages.Len(); i++ {
		msg := messages.Get(i)
		fullName := string(msg.FullName())
		index[fullName] = msg
		example, _ := json.MarshalIndent(exampleForMessage(msg, 0), "", "  ")
		*out = append(*out, TypeDescriptor{
			FullName:    fullName,
			Name:        string(msg.Name()),
			Package:     string(file.Package()),
			File:        file.Path(),
			PackID:      packID,
			Fields:      fieldInfos(msg),
			JSONSchema:  schemaForMessage(msg, 0),
			ExampleJSON: example,
		})
		collectMessageTypes(msg.Messages(), file, packID, out, index)
	}
}

func fieldInfos(msg protoreflect.MessageDescriptor) []FieldInfo {
	fields := msg.Fields()
	out := make([]FieldInfo, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		info := FieldInfo{
			Name:     string(field.Name()),
			JSONName: field.JSONName(),
			Type:     field.Kind().String(),
			Repeated: field.IsList(),
			Map:      field.IsMap(),
			Required: field.Cardinality() == protoreflect.Required,
		}
		if field.Message() != nil {
			info.MessageType = string(field.Message().FullName())
		}
		if field.Enum() != nil {
			info.EnumType = string(field.Enum().FullName())
		}
		out = append(out, info)
	}
	return out
}

func schemaForMessage(msg protoreflect.MessageDescriptor, depth int) map[string]any {
	properties := map[string]any{}
	fields := msg.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		properties[field.JSONName()] = schemaForField(field, depth)
	}
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
}

func schemaForField(field protoreflect.FieldDescriptor, depth int) map[string]any {
	if field.IsMap() {
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaForField(field.MapValue(), depth+1),
		}
	}
	if field.IsList() {
		return map[string]any{
			"type":  "array",
			"items": schemaForScalarOrMessage(field, depth+1),
		}
	}
	return schemaForScalarOrMessage(field, depth)
}

func schemaForScalarOrMessage(field protoreflect.FieldDescriptor, depth int) map[string]any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Int64Kind,
		protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed64Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		names := make([]string, 0, values.Len())
		for i := 0; i < values.Len(); i++ {
			names = append(names, string(values.Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": names}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if depth >= 3 {
			return map[string]any{"type": "object"}
		}
		return schemaForMessage(field.Message(), depth+1)
	default:
		return map[string]any{}
	}
}

func exampleForMessage(msg protoreflect.MessageDescriptor, depth int) map[string]any {
	out := map[string]any{}
	fields := msg.Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		out[field.JSONName()] = exampleForField(field, depth)
	}
	return out
}

func exampleForField(field protoreflect.FieldDescriptor, depth int) any {
	if field.IsMap() {
		return map[string]any{"key": exampleForField(field.MapValue(), depth+1)}
	}
	if field.IsList() {
		return []any{exampleForScalarOrMessage(field, depth+1)}
	}
	return exampleForScalarOrMessage(field, depth)
}

func exampleForScalarOrMessage(field protoreflect.FieldDescriptor, depth int) any {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return true
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Int64Kind,
		protoreflect.Sint64Kind, protoreflect.Sfixed64Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed64Kind:
		return 1
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return 1.5
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BytesKind:
		return base64.StdEncoding.EncodeToString([]byte("bytes"))
	case protoreflect.EnumKind:
		values := field.Enum().Values()
		if values.Len() > 0 {
			return string(values.Get(0).Name())
		}
		return ""
	case protoreflect.MessageKind, protoreflect.GroupKind:
		if depth >= 2 {
			return map[string]any{}
		}
		return exampleForMessage(field.Message(), depth+1)
	default:
		return nil
	}
}

func extractZip(zipPath, dest string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	destClean, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, file := range reader.File {
		target := filepath.Join(dest, file.Name)
		targetClean, err := filepath.Abs(target)
		if err != nil {
			return err
		}
		if targetClean != destClean && !strings.HasPrefix(targetClean, destClean+string(os.PathSeparator)) {
			return fmt.Errorf("zip entry %q escapes schema pack", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(targetClean, 0o755); err != nil {
				return err
			}
			continue
		}
		if strings.ToLower(filepath.Ext(file.Name)) != ".proto" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetClean), 0o755); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(src, 32<<20))
		closeErr := src.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.WriteFile(targetClean, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func sanitizePackName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r)
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func uniquePackID(name string) string {
	return fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
}
