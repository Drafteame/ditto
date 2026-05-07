package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
	"nhooyr.io/websocket"
)

func TestSchemaRegistryLoadsProtoAndEncodesPayload(t *testing.T) {
	root := t.TempDir()
	packDir := filepath.Join(root, "example-pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	protoFile := `syntax = "proto3";
package ditto.test;

message ScoreEvent {
  string match_id = 1;
  int32 home_score = 2;
  repeated string tags = 3;
}
`
	if err := os.WriteFile(filepath.Join(packDir, "score.proto"), []byte(protoFile), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	types := reg.Types()
	if len(types) != 1 || types[0].FullName != "ditto.test.ScoreEvent" {
		t.Fatalf("Types() = %#v, want ditto.test.ScoreEvent", types)
	}

	encoded, err := reg.Encode("ditto.test.ScoreEvent", json.RawMessage(`{
  "matchId": "abc",
  "homeScore": 2,
  "tags": ["live"]
}`))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if encoded.ContentType != "application/x-protobuf" || encoded.TypeName != "ditto.test.ScoreEvent" {
		t.Fatalf("Encode() metadata = %#v", encoded)
	}

	desc := reg.Descriptor("ditto.test.ScoreEvent")
	msg := dynamicpb.NewMessage(desc)
	if err := proto.Unmarshal(encoded.Data, msg); err != nil {
		t.Fatalf("encoded payload is not valid protobuf: %v", err)
	}
	if got := msg.Get(desc.Fields().ByName("match_id")).String(); got != "abc" {
		t.Fatalf("match_id = %q, want abc", got)
	}
	if got := msg.Get(desc.Fields().ByName("home_score")).Int(); got != 2 {
		t.Fatalf("home_score = %d, want 2", got)
	}

	decoded, err := reg.Decode("ditto.test.ScoreEvent", encoded.Data)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !strings.Contains(string(decoded), `"matchId":"abc"`) {
		t.Fatalf("Decode() = %s, want matchId", decoded)
	}
}

func TestSchemaRegistrySkipsBrokenPackOnLoad(t *testing.T) {
	root := t.TempDir()
	writeProto(t, filepath.Join(root, "good"), "good.proto", `syntax = "proto3"; package ditto.good; message Event { string id = 1; }`)
	writeProto(t, filepath.Join(root, "bad"), "bad.proto", `syntax = "proto3"; package ditto.bad; message Broken { string id = 1;`)

	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	types := reg.Types()
	if len(types) != 1 || types[0].FullName != "ditto.good.Event" {
		t.Fatalf("Types() = %#v, want only good pack", types)
	}
}

func TestSchemaRegistryFailedPackDoesNotPoisonRegistry(t *testing.T) {
	root := t.TempDir()
	first := writeProto(t, filepath.Join(root, "first"), "event.proto", `syntax = "proto3"; package ditto.same; message Event { string id = 1; }`)
	second := writeProto(t, filepath.Join(root, "second"), "event.proto", `syntax = "proto3"; package ditto.same; message Event { int32 id = 1; }`)

	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	before := reg.Types()
	if len(before) != 1 {
		t.Fatalf("Types() len = %d, want 1", len(before))
	}

	if _, err := reg.RegisterPack(second); err == nil {
		t.Fatalf("RegisterPack(second) unexpectedly succeeded")
	}
	after := reg.Types()
	if len(after) != 1 || after[0].FullName != before[0].FullName {
		t.Fatalf("Types() after failed register = %#v, want unchanged %#v", after, before)
	}

	if _, err := reg.RegisterPack(first); err == nil {
		t.Fatalf("RegisterPack(first) unexpectedly succeeded for duplicate pack id")
	}
}

func TestSchemaRegistryRejectsDifferentDescriptorsWithSamePath(t *testing.T) {
	root := t.TempDir()
	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	first := writeProto(t, filepath.Join(root, "a-pack"), "event.proto", `syntax = "proto3"; package ditto.a; message Event { string id = 1; }`)
	second := writeProto(t, filepath.Join(root, "b-pack"), "event.proto", `syntax = "proto3"; package ditto.b; message Event { string id = 1; }`)

	if _, err := reg.RegisterPack(first); err != nil {
		t.Fatalf("RegisterPack(first) error = %v", err)
	}
	if _, err := reg.RegisterPack(second); err == nil || !strings.Contains(err.Error(), "already registered with different contents") {
		t.Fatalf("RegisterPack(second) error = %v, want path collision", err)
	}
}

func TestSchemaRegistryZipUploadPreservesManifest(t *testing.T) {
	root := t.TempDir()
	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addZipFile(t, zw, "bundle/manifest.json", `{"manifest_version":1,"id":"custom-pack","name":"Custom Pack","version":"1.2.3"}`)
	addZipFile(t, zw, "bundle/event.proto", `syntax = "proto3"; package ditto.zip; message Event { string id = 1; }`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	pack, err := reg.ImportUploadedPack("events.zip", bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("ImportUploadedPack() error = %v", err)
	}
	if pack.ID != "custom-pack" || pack.Name != "custom-pack" || pack.Version != "1.2.3" {
		t.Fatalf("pack metadata = %#v", pack)
	}
	if _, err := os.Stat(filepath.Join(root, "custom-pack", "manifest.json")); err != nil {
		t.Fatalf("manifest was not preserved: %v", err)
	}
}

func TestSchemaRegistryRejectsUnsupportedManifestVersion(t *testing.T) {
	root := t.TempDir()
	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	dir := writeProto(t, filepath.Join(root, "bad-version"), "event.proto", `syntax = "proto3"; package ditto.badversion; message Event { string id = 1; }`)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version":2,"name":"bad"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.RegisterPack(dir); err == nil || !strings.Contains(err.Error(), "unsupported manifest_version 2 (expected 1)") {
		t.Fatalf("RegisterPack() error = %v, want manifest version rejection", err)
	}
}

func TestSchemaRegistryRejectsManifestWithoutVersion(t *testing.T) {
	root := t.TempDir()
	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	dir := writeProto(t, filepath.Join(root, "missing-version"), "event.proto", `syntax = "proto3"; package ditto.missingversion; message Event { string id = 1; }`)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"name":"missing"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := reg.RegisterPack(dir); err == nil || !strings.Contains(err.Error(), "manifest.json requires manifest_version: 1") {
		t.Fatalf("RegisterPack() error = %v, want missing manifest_version message", err)
	}
}

func TestSchemaRegistryRejectsPackOutsideRegistryDir(t *testing.T) {
	root := t.TempDir()
	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	outside := writeProto(t, filepath.Join(t.TempDir(), "outside"), "event.proto", `syntax = "proto3"; package ditto.outside; message Event { string id = 1; }`)

	if _, err := reg.RegisterPack(outside); err == nil || !strings.Contains(err.Error(), "must be inside") {
		t.Fatalf("RegisterPack(outside) error = %v, want managed path rejection", err)
	}
}

func TestSchemaRegistryLoadSkipsHiddenUploadDir(t *testing.T) {
	root := t.TempDir()
	writeProto(t, filepath.Join(root, ".upload-events"), "event.proto", `syntax = "proto3"; package ditto.hidden; message Event { string id = 1; }`)

	reg, err := NewSchemaRegistry(root)
	if err != nil {
		t.Fatalf("NewSchemaRegistry() error = %v", err)
	}
	if got := reg.Types(); len(got) != 0 {
		t.Fatalf("Types() = %#v, want hidden upload dir skipped", got)
	}
}

func TestNormalizeExtractedPackLayoutFlattensNestedSingleWrapper(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "outer", "inner")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifest_version":1,"name":"nested"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "event.proto"), []byte(`syntax = "proto3"; package ditto.nested; message Event { string id = 1; }`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := normalizeExtractedPackLayout(root); err != nil {
		t.Fatalf("normalizeExtractedPackLayout() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "manifest.json")); err != nil {
		t.Fatalf("manifest not flattened: %v", err)
	}
}

func TestNormalizeExtractedPackLayoutRejectsAmbiguousNestedRoots(t *testing.T) {
	root := t.TempDir()
	writeProto(t, filepath.Join(root, "a"), "event.proto", `syntax = "proto3"; package ditto.a; message Event { string id = 1; }`)
	writeProto(t, filepath.Join(root, "b"), "event.proto", `syntax = "proto3"; package ditto.b; message Event { string id = 1; }`)

	if err := normalizeExtractedPackLayout(root); err == nil || !strings.Contains(err.Error(), "single wrapper directory") {
		t.Fatalf("normalizeExtractedPackLayout() error = %v, want ambiguous layout rejection", err)
	}
}

func TestExtractZipRejectsUnpackedLimit(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	addZipFile(t, zw, "a.proto", `syntax = "proto3"; package ditto.limit; message A { string id = 1; }`)
	addZipFile(t, zw, "b.proto", `syntax = "proto3"; package ditto.limit; message B { string id = 1; }`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(t.TempDir(), "pack.zip")
	if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	err := extractZipWithLimit(zipPath, t.TempDir(), 10)
	if err == nil || !strings.Contains(err.Error(), "unpacked limit") {
		t.Fatalf("extractZipWithLimit() error = %v, want unpacked limit", err)
	}
}

func TestAppSyncWrapsBinaryPayloadAsBase64(t *testing.T) {
	payload := EncodedPayload{
		Data:        []byte{8, 7},
		Kind:        websocket.MessageBinary,
		ContentType: "application/x-protobuf",
		TypeName:    "ditto.test.ScoreEvent",
	}
	msg, err := AppSyncAdapter{}.WrapData(payload, "sub-1")
	if err != nil {
		t.Fatalf("WrapData() error = %v", err)
	}
	var env struct {
		Payload struct {
			Data struct {
				Base64      string `json:"base64"`
				ContentType string `json:"content_type"`
				TypeName    string `json:"type_name"`
			} `json:"data"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		t.Fatalf("WrapData() JSON error = %v", err)
	}
	if env.Payload.Data.Base64 != base64.StdEncoding.EncodeToString([]byte{8, 7}) {
		t.Fatalf("base64 = %q", env.Payload.Data.Base64)
	}
	if env.Payload.Data.ContentType != "application/x-protobuf" || env.Payload.Data.TypeName != "ditto.test.ScoreEvent" {
		t.Fatalf("metadata = %#v", env.Payload.Data)
	}
}

func writeProto(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func addZipFile(t *testing.T, zw *zip.Writer, name, body string) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}
