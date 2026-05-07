package main

import (
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
