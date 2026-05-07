package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
	"nhooyr.io/websocket"
)

const (
	maxSchemaUploadBytes = 128 << 20
	maxSchemaUnpackBytes = 256 << 20
	maxProtoFileBytes    = 32 << 20
	maxManifestBytes     = 1 << 20
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
	Version  string           `json:"version,omitempty"`
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
	ExampleJSON json.RawMessage `json:"example_json"`
}

type FieldInfo struct {
	Name        string `json:"name"`
	JSONName    string `json:"json_name"`
	Type        string `json:"type"`
	Number      int32  `json:"number"`
	Repeated    bool   `json:"repeated"`
	Map         bool   `json:"map"`
	Optional    bool   `json:"optional"`
	Oneof       string `json:"oneof,omitempty"`
	MessageType string `json:"message_type,omitempty"`
	EnumType    string `json:"enum_type,omitempty"`
}

type schemaPackManifest struct {
	ManifestVersion int      `json:"manifest_version"`
	ID              string   `json:"id,omitempty"`
	Name            string   `json:"name,omitempty"`
	Description     string   `json:"description,omitempty"`
	Version         string   `json:"version,omitempty"`
	DittoMinVersion string   `json:"ditto_min_version,omitempty"`
	Artifacts       struct{} `json:"artifacts,omitempty"`
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
		if isTransientSchemaDir(entry.Name()) {
			continue
		}
		packPath := filepath.Join(s.dir, entry.Name())
		if _, err := s.registerPackLocked(packPath); err != nil {
			log.Printf("schema pack %s skipped: %v", entry.Name(), err)
			continue
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

func (s *SchemaRegistry) DeletePack(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("schema pack id is required")
	}

	s.mu.RLock()
	pack, ok := s.packs[id]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("schema pack %q not found", id)
	}
	if err := os.RemoveAll(pack.Path); err != nil {
		return err
	}
	return s.Load()
}

func (s *SchemaRegistry) Descriptor(typeName string) protoreflect.MessageDescriptor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.types[strings.TrimSpace(typeName)]
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

func (s *SchemaRegistry) Decode(typeName string, data []byte) (json.RawMessage, error) {
	typeName = strings.TrimSpace(typeName)
	if typeName == "" {
		return nil, fmt.Errorf("type name is required")
	}

	s.mu.RLock()
	desc := s.types[typeName]
	resolver := s.resolver
	s.mu.RUnlock()
	if desc == nil {
		return nil, fmt.Errorf("schema type %q is not loaded", typeName)
	}

	msg := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err
	}
	out, err := (protojson.MarshalOptions{Resolver: resolver}).Marshal(msg)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *SchemaRegistry) ImportUploadedPack(filename string, reader io.Reader) (SchemaPack, error) {
	baseName := sanitizePackName(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if baseName == "" {
		baseName = "schema-pack"
	}
	tmpDir, err := os.MkdirTemp(s.dir, ".upload-"+baseName+"-*")
	if err != nil {
		return SchemaPack{}, err
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			os.RemoveAll(tmpDir)
		}
	}()

	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".proto":
		data, err := readLimited(reader, maxProtoFileBytes, filename)
		if err != nil {
			return SchemaPack{}, err
		}
		if err := os.WriteFile(filepath.Join(tmpDir, filepath.Base(filename)), data, 0o644); err != nil {
			return SchemaPack{}, err
		}
	case ".zip":
		tmp, err := os.CreateTemp("", "ditto-schema-*.zip")
		if err != nil {
			return SchemaPack{}, err
		}
		tmpPath := tmp.Name()
		if _, err := copyLimited(tmp, reader, maxSchemaUploadBytes, filename); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return SchemaPack{}, err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpPath)
			return SchemaPack{}, err
		}
		if err := extractZip(tmpPath, tmpDir); err != nil {
			os.Remove(tmpPath)
			return SchemaPack{}, err
		}
		os.Remove(tmpPath)
		if err := flattenSingleWrapperDir(tmpDir); err != nil {
			return SchemaPack{}, err
		}
	default:
		return SchemaPack{}, fmt.Errorf("upload a .proto file or a .zip schema pack")
	}

	metadata, err := schemaPackMetadata(tmpDir, baseName)
	if err != nil {
		return SchemaPack{}, err
	}
	dest := filepath.Join(s.dir, metadata.ID)
	if _, err := os.Stat(dest); err == nil {
		return SchemaPack{}, fmt.Errorf("schema pack %q already exists", metadata.ID)
	} else if !os.IsNotExist(err) {
		return SchemaPack{}, err
	}
	if err := os.Rename(tmpDir, dest); err != nil {
		return SchemaPack{}, err
	}
	cleanupTmp = false

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

	metadata, err := schemaPackMetadata(packPath, filepath.Base(packPath))
	if err != nil {
		return SchemaPack{}, err
	}
	if _, exists := s.packs[metadata.ID]; exists {
		return SchemaPack{}, fmt.Errorf("schema pack id %q is already loaded", metadata.ID)
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: []string{packPath},
		}),
	}
	files, err := compiler.Compile(context.Background(), protos...)
	if err != nil {
		return SchemaPack{}, err
	}

	candidateFiles := &protoregistry.Files{}
	if err := copyFiles(s.files, candidateFiles); err != nil {
		return SchemaPack{}, err
	}
	candidateTypes := copyTypeMap(s.types)
	candidatePacks := copyPackMap(s.packs)

	pack := SchemaPack{
		ID:       metadata.ID,
		Name:     metadata.Name,
		Version:  metadata.Version,
		Path:     packPath,
		LoadedAt: time.Now().Format(time.RFC3339),
		Types:    make([]TypeDescriptor, 0),
	}
	for _, file := range files {
		if err := registerFileRecursive(candidateFiles, file); err != nil {
			return SchemaPack{}, err
		}
	}

	for _, file := range files {
		if err := collectMessageTypes(file.Messages(), file, metadata.ID, &pack.Types, candidateTypes); err != nil {
			return SchemaPack{}, err
		}
	}
	sort.Slice(pack.Types, func(i, j int) bool {
		return pack.Types[i].FullName < pack.Types[j].FullName
	})
	candidatePacks[pack.ID] = pack

	s.files = candidateFiles
	s.types = candidateTypes
	s.packs = candidatePacks
	s.resolver = dynamicpb.NewTypes(s.files)
	return pack, nil
}

func (s *SchemaRegistry) resetDescriptorsLocked() {
	s.files = &protoregistry.Files{}
	s.resolver = dynamicpb.NewTypes(s.files)
}

func registerFileRecursive(files *protoregistry.Files, file protoreflect.FileDescriptor) error {
	if existing, err := files.FindFileByPath(file.Path()); err == nil {
		if !fileDescriptorEqual(existing, file) {
			return fmt.Errorf("schema file path %q is already registered with different contents", file.Path())
		}
		return nil
	}
	imports := file.Imports()
	for i := 0; i < imports.Len(); i++ {
		if err := registerFileRecursive(files, imports.Get(i)); err != nil {
			return err
		}
	}
	return files.RegisterFile(file)
}

func fileDescriptorEqual(a, b protoreflect.FileDescriptor) bool {
	return proto.Equal(protodesc.ToFileDescriptorProto(a), protodesc.ToFileDescriptorProto(b))
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
			r.Body = http.MaxBytesReader(w, r.Body, maxSchemaUploadBytes)
			if err := r.ParseMultipartForm(maxSchemaUploadBytes); err != nil {
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
	mux.HandleFunc("/__ditto__/api/schemas/packs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !isAllowedSocketAPIRequest(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		id := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/__ditto__/api/schemas/packs/"))
		if id == "" || id == "." || id == ".." || strings.Contains(id, "/") || strings.Contains(id, string(os.PathSeparator)) {
			http.NotFound(w, r)
			return
		}
		if err := schemas.DeletePack(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
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

type schemaPackMeta struct {
	ID      string
	Name    string
	Version string
}

func schemaPackMetadataFromManifest(root string) (schemaPackManifest, error) {
	path := filepath.Join(root, "manifest.json")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return schemaPackManifest{}, nil
	}
	if err != nil {
		return schemaPackManifest{}, err
	}
	defer file.Close()
	data, err := readLimited(file, maxManifestBytes, "manifest.json")
	if err != nil {
		return schemaPackManifest{}, err
	}
	var manifest schemaPackManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return schemaPackManifest{}, fmt.Errorf("invalid manifest.json: %w", err)
	}
	if manifest.ManifestVersion != 1 {
		return schemaPackManifest{}, fmt.Errorf("unsupported manifest_version %d", manifest.ManifestVersion)
	}
	return manifest, nil
}

func schemaPackMetadata(root, fallbackName string) (schemaPackMeta, error) {
	manifest, err := schemaPackMetadataFromManifest(root)
	if err != nil {
		return schemaPackMeta{}, err
	}
	contentID, err := contentHashPackID(root)
	if err != nil {
		return schemaPackMeta{}, err
	}

	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = fallbackName
	}
	name = sanitizePackName(name)
	if name == "" {
		name = "schema-pack"
	}

	id := sanitizePackName(manifest.ID)
	if id == "" {
		id = fmt.Sprintf("%s-%s", name, contentID[:12])
	}
	return schemaPackMeta{ID: id, Name: name, Version: manifest.Version}, nil
}

func contentHashPackID(root string) (string, error) {
	protos, err := protoFiles(root)
	if err != nil {
		return "", err
	}
	if len(protos) == 0 {
		return "", fmt.Errorf("no .proto files found")
	}
	hash := sha256.New()
	if manifest, err := readOptionalManifest(root); err == nil && len(manifest) > 0 {
		hash.Write([]byte("manifest.json"))
		hash.Write([]byte{0})
		hash.Write(manifest)
		hash.Write([]byte{0})
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	for _, rel := range protos {
		hash.Write([]byte(rel))
		hash.Write([]byte{0})
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		hash.Write(data)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readOptionalManifest(root string) ([]byte, error) {
	file, err := os.Open(filepath.Join(root, "manifest.json"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, maxManifestBytes, "manifest.json")
}

func copyFiles(src, dest *protoregistry.Files) error {
	var err error
	src.RangeFiles(func(file protoreflect.FileDescriptor) bool {
		err = registerFileRecursive(dest, file)
		return err == nil
	})
	return err
}

func copyTypeMap(src map[string]protoreflect.MessageDescriptor) map[string]protoreflect.MessageDescriptor {
	dest := make(map[string]protoreflect.MessageDescriptor, len(src))
	for key, value := range src {
		dest[key] = value
	}
	return dest
}

func copyPackMap(src map[string]SchemaPack) map[string]SchemaPack {
	dest := make(map[string]SchemaPack, len(src))
	for key, value := range src {
		dest[key] = value
	}
	return dest
}

func readLimited(reader io.Reader, limit int64, label string) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := copyLimited(&buf, reader, limit, label); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func copyLimited(dst io.Writer, src io.Reader, limit int64, label string) (int64, error) {
	written, err := io.Copy(dst, io.LimitReader(src, limit+1))
	if err != nil {
		return written, err
	}
	if written > limit {
		return written, fmt.Errorf("%s exceeds %dMB limit", label, limit/(1<<20))
	}
	return written, nil
}

func isTransientSchemaDir(name string) bool {
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
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

func collectMessageTypes(messages protoreflect.MessageDescriptors, file protoreflect.FileDescriptor, packID string, out *[]TypeDescriptor, index map[string]protoreflect.MessageDescriptor) error {
	for i := 0; i < messages.Len(); i++ {
		msg := messages.Get(i)
		fullName := string(msg.FullName())
		if _, exists := index[fullName]; exists {
			return fmt.Errorf("schema type %q is already loaded", fullName)
		}
		index[fullName] = msg
		example, _ := json.MarshalIndent(exampleForMessage(msg, 0), "", "  ")
		*out = append(*out, TypeDescriptor{
			FullName:    fullName,
			Name:        string(msg.Name()),
			Package:     string(file.Package()),
			File:        file.Path(),
			PackID:      packID,
			Fields:      fieldInfos(msg),
			ExampleJSON: example,
		})
		if err := collectMessageTypes(msg.Messages(), file, packID, out, index); err != nil {
			return err
		}
	}
	return nil
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
			Number:   int32(field.Number()),
			Repeated: field.IsList(),
			Map:      field.IsMap(),
			Optional: field.HasPresence() && field.Cardinality() == protoreflect.Optional,
		}
		if oneof := field.ContainingOneof(); oneof != nil {
			info.Oneof = string(oneof.Name())
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
	var unpacked int64
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
		allowed, limit := allowedSchemaZipEntry(file.Name)
		if !allowed {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(targetClean), 0o755); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		data, readErr := readLimited(src, limit, file.Name)
		closeErr := src.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		unpacked += int64(len(data))
		if unpacked > maxSchemaUnpackBytes {
			return fmt.Errorf("schema pack exceeds %dMB unpacked limit", maxSchemaUnpackBytes/(1<<20))
		}
		if err := os.WriteFile(targetClean, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func allowedSchemaZipEntry(name string) (bool, int64) {
	if strings.ToLower(filepath.Ext(name)) == ".proto" {
		return true, maxProtoFileBytes
	}
	if strings.EqualFold(filepath.Base(name), "manifest.json") {
		return true, maxManifestBytes
	}
	return false, 0
}

func flattenSingleWrapperDir(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return nil
	}
	wrapper := filepath.Join(root, entries[0].Name())
	children, err := os.ReadDir(wrapper)
	if err != nil {
		return err
	}
	for _, child := range children {
		if err := os.Rename(filepath.Join(wrapper, child.Name()), filepath.Join(root, child.Name())); err != nil {
			return err
		}
	}
	return os.Remove(wrapper)
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
