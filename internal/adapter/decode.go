package adapter

import (
	"encoding/base64"
	"encoding/json"

	"github.com/coder/websocket"

	"ditto/internal/schema"
)

// Decoded is the logical payload recovered from a frame, with the protobuf
// type and profile alias when the frame named them.
type Decoded struct {
	TypeName    string          `json:"type_name,omitempty"`
	PayloadJSON json.RawMessage `json:"payload_json,omitempty"`
	Alias       string          `json:"alias,omitempty"`
}

// DecodeFrame decodes a text frame's envelope for the named adapter so logs
// and recordings can show the logical payload. Binary frames are not decoded.
func (r *Registry) DecodeFrame(schemas *schema.Registry, kind string, data []byte, adapterName string) (*Decoded, string) {
	if kind == "binary" {
		return nil, "binary frame has no envelope decoder"
	}
	profileName := Normalize(adapterName)
	if profileName == "" {
		profileName = "raw"
	}
	if profile, ok := r.Profile(profileName); ok && hasEnvelopeDecoder(profile.BaseAdapter) {
		return decodeAppSyncEnvelope(schemas, profile, data)
	}
	if hasEnvelopeDecoder(profileName) {
		return decodeAppSyncEnvelope(schemas, Profile{BaseAdapter: profileName}, data)
	}
	var payload json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err.Error()
	}
	return &Decoded{PayloadJSON: payload}, ""
}

func hasEnvelopeDecoder(baseAdapter string) bool {
	switch Normalize(baseAdapter) {
	case "appsync", "relay":
		return true
	default:
		return false
	}
}

func decodeAppSyncEnvelope(schemas *schema.Registry, profile Profile, data []byte) (*Decoded, string) {
	var outer map[string]any
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, err.Error()
	}
	// AppSync emits periodic keep-alives and subscription-lifecycle frames
	// (ka, connection_ack, subscribe, subscribe_success, complete, error, …)
	// that carry no decodable payload. Surface them as control frames with the
	// type as the label instead of reporting a spurious decode error. Only
	// "data" frames carry a {t, e} payload to decode.
	if typ, ok := outer["type"].(string); ok && typ != "" && typ != "data" {
		return &Decoded{Alias: typ}, ""
	}
	value := outer["event"]
	if value == nil {
		if payload, ok := outer["payload"].(map[string]any); ok {
			if dataObj, ok := payload["data"].(map[string]any); ok {
				value = dataObj
			} else {
				value = payload["data"]
			}
		}
	}
	if text, ok := value.(string); ok {
		var inner map[string]any
		if err := json.Unmarshal([]byte(text), &inner); err != nil {
			return nil, err.Error()
		}
		value = inner
	}
	inner, ok := value.(map[string]any)
	if !ok {
		return nil, "appsync envelope not found"
	}
	alias, _ := inner["t"].(string)
	if encoded, ok := inner["base64"].(string); ok {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err.Error()
		}
		typeName, _ := inner["type_name"].(string)
		if typeName == "" {
			typeName = reverseAlias(profile.TypeAliases, alias)
		}
		if typeName == "" {
			typeName = alias
		}
		if typeName == "" || schemas == nil || schemas.Descriptor(typeName) == nil {
			return &Decoded{TypeName: typeName, Alias: alias}, ""
		}
		payload, err := schemas.Decode(typeName, raw)
		if err != nil {
			return nil, err.Error()
		}
		return &Decoded{TypeName: typeName, PayloadJSON: payload, Alias: alias}, ""
	}
	if alias == "" {
		return nil, "appsync alias missing"
	}
	rawValue, exists := inner["e"]
	if !exists {
		return nil, "appsync payload missing"
	}
	encoded, ok := rawValue.(string)
	if !ok {
		payload, err := json.Marshal(rawValue)
		if err != nil {
			return nil, err.Error()
		}
		return &Decoded{Alias: alias, PayloadJSON: payload}, ""
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err.Error()
	}
	typeName := reverseAlias(profile.TypeAliases, alias)
	if typeName == "" {
		typeName = alias
	}
	if schemas == nil || schemas.Descriptor(typeName) == nil {
		return &Decoded{Alias: alias}, ""
	}
	payload, err := schemas.Decode(typeName, raw)
	if err != nil {
		return nil, err.Error()
	}
	return &Decoded{TypeName: typeName, PayloadJSON: payload, Alias: alias}, ""
}

func reverseAlias(aliases map[string]string, alias string) string {
	for typeName, value := range aliases {
		if value == alias {
			return typeName
		}
	}
	return ""
}

// FrameKind names a WebSocket message type as recorded in logs: "binary"
// or "text".
func FrameKind(typ websocket.MessageType) string {
	if typ == websocket.MessageBinary {
		return "binary"
	}
	return "text"
}
