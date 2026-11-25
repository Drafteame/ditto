package adapter

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"nhooyr.io/websocket"
)

func textMessage(data []byte) Message {
	return Message{Data: data, Kind: websocket.MessageText}
}

func marshalTextMessage(v any) (Message, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return Message{}, err
	}
	return textMessage(data), nil
}

func rawPayload(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func appSyncErrorPayload(raw json.RawMessage) any {
	message := "socket error"
	if len(raw) > 0 {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			if text, ok := value.(string); ok && text != "" {
				message = text
			} else if obj, ok := value.(map[string]any); ok {
				if value, ok := obj["error"].(string); ok && value != "" {
					message = value
				} else if value, ok := obj["message"].(string); ok && value != "" {
					message = value
				}
			}
		} else {
			message = string(raw)
		}
	}
	return map[string]any{
		"errors": []map[string]string{{"message": message}},
	}
}

func newBuiltin(name string) (Protocol, error) {
	switch Normalize(name) {
	case "", "raw":
		return Raw{}, nil
	case "appsync":
		return AppSync{}, nil
	case "relay":
		return Relay{}, nil
	default:
		return nil, fmt.Errorf("unsupported socket adapter %q", name)
	}
}

// Normalize lowercases and trims an adapter name.
func Normalize(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// Raw is the plain-JSON protocol: clients send {"type","id","channel"}
// control frames and events are delivered unwrapped.
type Raw struct{}

func (Raw) ParseClientMessage(b []byte) (ClientMessage, error) {
	var env struct {
		Type    string          `json:"type"`
		Action  string          `json:"action"`
		Op      string          `json:"op"`
		ID      string          `json:"id"`
		Channel string          `json:"channel"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return ClientMessage{}, err
	}
	msgType := firstNonEmpty(env.Type, env.Action, env.Op)
	msgType = normalizeClientMessageType(msgType)
	if msgType == "" && env.Channel != "" {
		msgType = "subscribe"
	}
	if msgType == "" {
		return ClientMessage{}, errors.New("message missing type")
	}
	return ClientMessage{
		Type:           msgType,
		ID:             env.ID,
		Channel:        env.Channel,
		Payload:        env.Payload,
		SubscriptionID: env.ID,
	}, nil
}

func (Raw) EncodeServerMessage(msg ServerMessage) (Message, error) {
	switch msg.Type {
	case "data", "":
		payload, err := Raw{}.EncodePayload(msg.Payload)
		if err != nil {
			return Message{}, err
		}
		return Raw{}.WrapData(payload, msg.ID, msg.Channel)
	case "connection_ack":
		return marshalTextMessage(map[string]any{"type": "connection_ack"})
	case "subscribe_ack":
		return marshalTextMessage(map[string]any{"type": "subscribe_ack", "channel": msg.Channel})
	case "pong":
		return marshalTextMessage(map[string]any{"type": "pong"})
	case "error":
		return marshalTextMessage(map[string]any{"type": "error", "id": msg.ID, "payload": rawPayload(msg.Payload)})
	default:
		return marshalTextMessage(map[string]any{"type": msg.Type})
	}
}

func (Raw) EncodePayload(payload json.RawMessage) (Payload, error) {
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return Payload{
		Data: append([]byte(nil), payload...),
		Kind: websocket.MessageText,
	}, nil
}

func (Raw) WrapData(payload Payload, subID, channel string) (Message, error) {
	return Message{Data: payload.Data, Kind: payload.Kind}, nil
}

func (Raw) Heartbeat() (Message, time.Duration) {
	return textMessage([]byte(`{"type":"ping"}`)), 30 * time.Second
}

func (Raw) Subprotocols() []string {
	return nil
}

// AppSync is the AWS AppSync Events (graphql-ws style) protocol.
type AppSync struct{}

func (AppSync) ParseClientMessage(b []byte) (ClientMessage, error) {
	var env struct {
		Type    string          `json:"type"`
		ID      string          `json:"id"`
		Channel string          `json:"channel"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return ClientMessage{}, err
	}

	msgType := normalizeClientMessageType(env.Type)
	if msgType == "" {
		return ClientMessage{}, errors.New("message missing type")
	}

	channel := strings.TrimSpace(env.Channel)
	if channel == "" && len(env.Payload) > 0 {
		channel = channelFromPayload(env.Payload)
	}

	return ClientMessage{
		Type:           msgType,
		ID:             env.ID,
		Channel:        channel,
		Payload:        env.Payload,
		SubscriptionID: env.ID,
	}, nil
}

func (AppSync) EncodeServerMessage(msg ServerMessage) (Message, error) {
	switch msg.Type {
	case "connection_ack":
		return marshalTextMessage(map[string]any{
			"type":    "connection_ack",
			"payload": map[string]any{"connectionTimeoutMs": 300000},
		})
	case "subscribe_ack":
		return marshalTextMessage(map[string]any{"type": "subscribe_success", "id": msg.ID})
	case "pong":
		return marshalTextMessage(map[string]any{"type": "pong"})
	case "error":
		return marshalTextMessage(map[string]any{"type": "error", "id": msg.ID, "payload": appSyncErrorPayload(msg.Payload)})
	case "data", "":
		payload, err := AppSync{}.EncodePayload(msg.Payload)
		if err != nil {
			return Message{}, err
		}
		return AppSync{}.WrapData(payload, msg.ID, msg.Channel)
	default:
		return marshalTextMessage(map[string]any{"type": msg.Type, "id": msg.ID})
	}
}

func (AppSync) EncodePayload(payload json.RawMessage) (Payload, error) {
	var value any = map[string]any{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &value); err != nil {
			value = string(payload)
		}
	}
	return Payload{Value: value, Kind: websocket.MessageText}, nil
}

func (AppSync) WrapData(payload Payload, subID, channel string) (Message, error) {
	value := payload.Value
	if payload.Kind == websocket.MessageBinary {
		value = map[string]any{
			"base64":       base64.StdEncoding.EncodeToString(payload.Data),
			"content_type": payload.ContentType,
			"type_name":    payload.TypeName,
		}
	}
	return marshalTextMessage(map[string]any{
		"type": "data",
		"id":   subID,
		"payload": map[string]any{
			"data": value,
		},
	})
}

func (AppSync) Heartbeat() (Message, time.Duration) {
	return textMessage([]byte(`{"type":"ka"}`)), 5 * time.Second
}

func (AppSync) Subprotocols() []string {
	return []string{"aws-appsync-event-ws"}
}

// Relay is for backends that acknowledge on open and key subscription
// acks by channel.
type Relay struct{}

func (Relay) ParseClientMessage(b []byte) (ClientMessage, error) {
	return Raw{}.ParseClientMessage(b)
}

func (Relay) EncodeServerMessage(msg ServerMessage) (Message, error) {
	switch msg.Type {
	case "connection_ack":
		return marshalTextMessage(map[string]any{"type": "connection_ack"})
	case "subscribe_ack":
		return marshalTextMessage(map[string]any{"type": "subscribe_success", "id": msg.ID, "channel": msg.Channel})
	case "pong":
		return marshalTextMessage(map[string]any{"type": "pong"})
	case "error":
		return marshalTextMessage(map[string]any{"type": "error", "id": msg.ID, "channel": msg.Channel, "payload": rawPayload(msg.Payload)})
	case "data", "":
		payload, err := Relay{}.EncodePayload(msg.Payload)
		if err != nil {
			return Message{}, err
		}
		return Relay{}.WrapData(payload, msg.ID, msg.Channel)
	default:
		return marshalTextMessage(map[string]any{"type": msg.Type, "channel": msg.Channel})
	}
}

func (Relay) EncodePayload(payload json.RawMessage) (Payload, error) {
	return AppSync{}.EncodePayload(payload)
}

func (Relay) WrapData(payload Payload, subID, channel string) (Message, error) {
	value := payload.Value
	if payload.Kind == websocket.MessageBinary {
		value = map[string]any{
			"base64":       base64.StdEncoding.EncodeToString(payload.Data),
			"content_type": payload.ContentType,
			"type_name":    payload.TypeName,
		}
	}
	return marshalTextMessage(map[string]any{
		"type":    "data",
		"channel": channel,
		"event":   value,
	})
}

func (Relay) Heartbeat() (Message, time.Duration) {
	return textMessage([]byte(`{"type":"ka"}`)), 5 * time.Second
}

func (Relay) Subprotocols() []string {
	return nil
}

func (Relay) GreetsOnConnect() bool {
	return true
}

// GreetsOnConnect reports whether protocol acknowledges a connection on open.
func GreetsOnConnect(protocol Protocol) bool {
	greeter, ok := protocol.(Greeter)
	return ok && greeter.GreetsOnConnect()
}

func normalizeClientMessageType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "connection_init", "init":
		return "connection_init"
	case "subscribe", "start":
		return "subscribe"
	case "unsubscribe", "stop", "complete":
		return "unsubscribe"
	case "ping":
		return "ping"
	default:
		return strings.ToLower(strings.TrimSpace(t))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func channelFromPayload(payload json.RawMessage) string {
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"channel", "path", "topic"} {
		if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if ext, ok := obj["extensions"].(map[string]any); ok {
		for _, key := range []string{"channel", "path", "topic"} {
			if value, ok := ext[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}
