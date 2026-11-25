// Package schema loads user-supplied protobuf schema packs (.proto files,
// optionally zipped with a manifest) at runtime and converts payloads
// between protojson and protobuf wire bytes with dynamic descriptors.
package schema

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
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"

	"ditto/internal/jsonfile"
)

// MaxUploadBytes is the largest schema pack upload accepted.
const MaxUploadBytes = 128 << 20

const (
	maxSchemaUnpackBytes = 256 << 20
	maxProtoFileBytes    = 32 << 20
	maxManifestBytes     = 1 << 20
)

// Registry holds the schema packs found in one directory and the message
// types they define, indexed by full name. It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	dir      string
	packs    map[string]Pack
	types    map[string]protoreflect.MessageDescriptor
	files    *protoregistry.Files
	resolver *dynamicpb.Types
}

// Pack is one loaded schema pack.
type Pack struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Path     string `json:"path"`
	LoadedAt string `json:"loaded_at"`
	Types    []Type `json:"types"`
}

// Type describes a message type for the dashboard: its fields and an
// example payload.
type Type struct {
	FullName    string          `json:"full_name"`
	Name        string          `json:"name"`
	Package     string          `json:"package"`
	File        string          `json:"file"`
	PackID      string          `json:"pack_id"`
	Fields      []Field         `json:"fields"`
	ExampleJSON json.RawMessage `json:"example_json"`
}

// Field describes one field of a Type.
type Field struct {
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
	ManifestVersion int    `json:"manifest_version"`
	ID              string `json:"id,omitempty"`
	Name            string `json:"name,omitempty"`
	Description     string `json:"description,omitempty"`
	Version         string `json:"version,omitempty"`
	DittoMinVersion string `json:"ditto_min_version,omitempty"`
}

// NewRegistry creates dir if needed and loads the packs in it.
func NewRegistry(dir string) (*Registry, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return nil, err
	}
	reg := &Registry{
		dir:   absDir,
		packs: make(map[string]Pack),
		types: make(map[string]protoreflect.MessageDescriptor),
	}
	reg.resetDescriptorsLocked()
	if err := reg.Load(); err != nil {
		return nil, err
	}
	return reg, nil
}

// Load re-reads every pack in the directory. A pack that fails to compile
// is skipped with a log line and does not affect the others.
func (s *Registry) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.packs = make(map[string]Pack)
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
			slog.Warn("schema pack skipped", "file", entry.Name(), "err", err)
			continue
		}
	}
	return nil
}

// Packs returns the loaded packs.
func (s *Registry) Packs() []Pack {
	s.mu.RLock()
	defer s.mu.RUnlock()

	packs := make([]Pack, 0, len(s.packs))
	for _, pack := range s.packs {
		packs = append(packs, pack)
	}
	sort.Slice(packs, func(i, j int) bool {
		return packs[i].Name < packs[j].Name
	})
	return packs
}

// Types returns every message type across the loaded packs.
func (s *Registry) Types() []Type {
	s.mu.RLock()
	defer s.mu.RUnlock()

	types := make([]Type, 0)
	for _, pack := range s.packs {
		types = append(types, pack.Types...)
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i].FullName < types[j].FullName
	})
	return types
}

// RegisterPack loads the pack at path, which must be inside the registry
// directory, and adds its types.
func (s *Registry) RegisterPack(path string) (Pack, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registerPackLocked(path)
}

// DeletePack removes the pack's files and reloads the remaining packs.
func (s *Registry) DeletePack(id string) error {
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

// Descriptor returns the message descriptor for typeName, or nil.
func (s *Registry) Descriptor(typeName string) protoreflect.MessageDescriptor {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.types[strings.TrimSpace(typeName)]
}

// Encode converts a protojson payload to protobuf wire bytes for typeName.
// An empty payload encodes the zero message.
func (s *Registry) Encode(typeName string, payload json.RawMessage) ([]byte, error) {
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
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	if err := (protojson.UnmarshalOptions{Resolver: resolver}).Unmarshal(payload, msg); err != nil {
		return nil, err
	}
	return proto.Marshal(msg)
}

// Decode converts protobuf wire bytes of typeName to protojson.
func (s *Registry) Decode(typeName string, data []byte) (json.RawMessage, error) {
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

// Import stores an uploaded .proto file or .zip pack under the registry
// directory and loads it, enforcing the size and unpack limits.
func (s *Registry) Import(filename string, reader io.Reader) (Pack, error) {
	baseName := jsonfile.Slug(strings.TrimSuffix(filename, filepath.Ext(filename)))
	if baseName == "" {
		baseName = "schema-pack"
	}
	tmpDir, err := os.MkdirTemp("", "ditto-schema-upload-"+baseName+"-*")
	if err != nil {
		return Pack{}, err
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
			return Pack{}, err
		}
		if err := os.WriteFile(filepath.Join(tmpDir, filepath.Base(filename)), data, 0o644); err != nil {
			return Pack{}, err
		}
	case ".zip":
		tmp, err := os.CreateTemp("", "ditto-schema-*.zip")
		if err != nil {
			return Pack{}, err
		}
		tmpPath := tmp.Name()
		if _, err := copyLimited(tmp, reader, MaxUploadBytes, filename); err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return Pack{}, err
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmpPath)
			return Pack{}, err
		}
		if err := extractZip(tmpPath, tmpDir); err != nil {
			os.Remove(tmpPath)
			return Pack{}, err
		}
		os.Remove(tmpPath)
		if err := normalizeExtractedPackLayout(tmpDir); err != nil {
			return Pack{}, err
		}
	default:
		return Pack{}, fmt.Errorf("upload a .proto file or a .zip schema pack")
	}

	metadata, err := schemaPackMetadata(tmpDir, baseName)
	if err != nil {
		return Pack{}, err
	}
	dest := filepath.Join(s.dir, metadata.ID)
	if _, err := os.Stat(dest); err == nil {
		return Pack{}, fmt.Errorf("schema pack %q already exists", metadata.ID)
	} else if !os.IsNotExist(err) {
		return Pack{}, err
	}
	if err := moveDir(tmpDir, dest); err != nil {
		return Pack{}, err
	}
	cleanupTmp = false

	pack, err := s.RegisterPack(dest)
	if err != nil {
		os.RemoveAll(dest)
		return Pack{}, err
	}
	return pack, nil
}

func (s *Registry) registerPackLocked(packPath string) (Pack, error) {
	managedPath, err := s.managedPackPath(packPath)
	if err != nil {
		return Pack{}, err
	}
	packPath = managedPath

	protos, err := protoFiles(packPath)
	if err != nil {
		return Pack{}, err
	}
	if len(protos) == 0 {
		return Pack{}, fmt.Errorf("no .proto files found")
	}

	metadata, err := schemaPackMetadata(packPath, filepath.Base(packPath))
	if err != nil {
		return Pack{}, err
	}
	if _, exists := s.packs[metadata.ID]; exists {
		return Pack{}, fmt.Errorf("schema pack id %q is already loaded", metadata.ID)
	}

	compiler := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
			ImportPaths: []string{packPath},
		}),
	}
	files, err := compiler.Compile(context.Background(), protos...)
	if err != nil {
		return Pack{}, err
	}

	candidateFiles := &protoregistry.Files{}
	if err := copyFiles(s.files, candidateFiles); err != nil {
		return Pack{}, err
	}
	candidateTypes := copyTypeMap(s.types)
	candidatePacks := copyPackMap(s.packs)

	pack := Pack{
		ID:       metadata.ID,
		Name:     metadata.Name,
		Version:  metadata.Version,
		Path:     packPath,
		LoadedAt: time.Now().Format(time.RFC3339),
		Types:    make([]Type, 0),
	}
	for _, file := range files {
		if err := registerFileRecursive(candidateFiles, file); err != nil {
			return Pack{}, err
		}
	}

	for _, file := range files {
		if err := collectMessageTypes(file.Messages(), file, metadata.ID, &pack.Types, candidateTypes); err != nil {
			return Pack{}, err
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

func (s *Registry) managedPackPath(packPath string) (string, error) {
	absPath, err := filepath.Abs(packPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.dir, absPath)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("schema pack path %q must be inside %s", packPath, s.dir)
	}
	if strings.Contains(rel, string(os.PathSeparator)) {
		return "", fmt.Errorf("schema pack path %q must be a direct child of %s", packPath, s.dir)
	}
	if isTransientSchemaDir(filepath.Base(absPath)) {
		return "", fmt.Errorf("schema pack path %q is reserved for temporary data", packPath)
	}
	return absPath, nil
}

func (s *Registry) resetDescriptorsLocked() {
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
	ap := protodesc.ToFileDescriptorProto(a)
	bp := protodesc.ToFileDescriptorProto(b)
	ap.SourceCodeInfo = nil
	bp.SourceCodeInfo = nil
	return proto.Equal(ap, bp)
}

type schemaPackMeta struct {
	ID      string
	Name    string
	Version string
}

func schemaPackMetadataFromManifest(root string) (schemaPackManifest, []byte, error) {
	path := filepath.Join(root, "manifest.json")
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return schemaPackManifest{}, nil, nil
	}
	if err != nil {
		return schemaPackManifest{}, nil, err
	}
	defer file.Close()
	data, err := readLimited(file, maxManifestBytes, "manifest.json")
	if err != nil {
		return schemaPackManifest{}, nil, err
	}
	var manifest schemaPackManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return schemaPackManifest{}, nil, fmt.Errorf("invalid manifest.json: %w", err)
	}
	if manifest.ManifestVersion == 0 {
		return schemaPackManifest{}, nil, fmt.Errorf("manifest.json requires manifest_version: 1")
	}
	if manifest.ManifestVersion != 1 {
		return schemaPackManifest{}, nil, fmt.Errorf("unsupported manifest_version %d (expected 1)", manifest.ManifestVersion)
	}
	return manifest, data, nil
}

func schemaPackMetadata(root, fallbackName string) (schemaPackMeta, error) {
	manifest, manifestData, err := schemaPackMetadataFromManifest(root)
	if err != nil {
		return schemaPackMeta{}, err
	}
	contentID, err := contentHashPackID(root, manifestData)
	if err != nil {
		return schemaPackMeta{}, err
	}

	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = fallbackName
	}
	name = jsonfile.Slug(name)
	if name == "" {
		name = "schema-pack"
	}

	id := jsonfile.Slug(manifest.ID)
	if id == "" {
		id = fmt.Sprintf("%s-%s", name, contentID[:12])
	}
	return schemaPackMeta{ID: id, Name: name, Version: manifest.Version}, nil
}

func contentHashPackID(root string, manifestData []byte) (string, error) {
	protos, err := protoFiles(root)
	if err != nil {
		return "", err
	}
	if len(protos) == 0 {
		return "", fmt.Errorf("no .proto files found")
	}
	hash := sha256.New()
	if len(manifestData) > 0 {
		hash.Write([]byte("manifest.json"))
		hash.Write([]byte{0})
		hash.Write(manifestData)
		hash.Write([]byte{0})
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

func copyPackMap(src map[string]Pack) map[string]Pack {
	dest := make(map[string]Pack, len(src))
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

func moveDir(src, dest string) error {
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("destination %q already exists", dest)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := copyDir(src, dest); err != nil {
		os.RemoveAll(dest)
		return err
	}
	if err := os.RemoveAll(src); err != nil {
		os.RemoveAll(dest)
		return err
	}
	return nil
}

func copyDir(src, dest string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
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

func collectMessageTypes(messages protoreflect.MessageDescriptors, file protoreflect.FileDescriptor, packID string, out *[]Type, index map[string]protoreflect.MessageDescriptor) error {
	for i := 0; i < messages.Len(); i++ {
		msg := messages.Get(i)
		fullName := string(msg.FullName())
		if _, exists := index[fullName]; exists {
			return fmt.Errorf("schema type %q is already loaded", fullName)
		}
		index[fullName] = msg
		example, _ := json.MarshalIndent(exampleForMessage(msg, 0), "", "  ")
		*out = append(*out, Type{
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

func fieldInfos(msg protoreflect.MessageDescriptor) []Field {
	fields := msg.Fields()
	out := make([]Field, 0, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		info := Field{
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
	return extractZipWithLimit(zipPath, dest, maxSchemaUnpackBytes)
}

func extractZipWithLimit(zipPath, dest string, maxUnpackBytes int64) error {
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
		if unpacked > maxUnpackBytes {
			return fmt.Errorf("schema pack exceeds %dMB unpacked limit", maxUnpackBytes/(1<<20))
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

func normalizeExtractedPackLayout(root string) error {
	for {
		hasRootArtifact, err := hasSchemaRootArtifact(root)
		if err != nil {
			return err
		}
		if hasRootArtifact {
			return nil
		}

		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		var dirs []os.DirEntry
		for _, entry := range entries {
			if entry.IsDir() {
				dirs = append(dirs, entry)
			}
		}
		if len(dirs) != 1 || len(entries) != 1 {
			return fmt.Errorf("schema pack zip must contain .proto files or manifest.json at its root, or a single wrapper directory")
		}

		wrapper := filepath.Join(root, dirs[0].Name())
		children, err := os.ReadDir(wrapper)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := os.Rename(filepath.Join(wrapper, child.Name()), filepath.Join(root, child.Name())); err != nil {
				return err
			}
		}
		if err := os.Remove(wrapper); err != nil {
			return err
		}
	}
}

func hasSchemaRootArtifact(root string) (bool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if allowed, _ := allowedSchemaZipEntry(entry.Name()); allowed {
			return true, nil
		}
	}
	return false, nil
}
