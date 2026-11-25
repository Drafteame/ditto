// Package adapter speaks the WebSocket wire protocols Ditto mocks: the raw,
// AppSync and Relay built-ins and JSON-configured profiles that wrap them
// with custom envelopes and type aliases. It parses client messages, wraps
// outgoing payloads and decodes frames back into logical payloads.
package adapter

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"nhooyr.io/websocket"
)

// ClientMessage is a parsed client frame: a subscribe, unsubscribe, ping
// or other control message, normalised across protocols.
type ClientMessage struct {
	Type           string
	ID             string
	Channel        string
	Payload        json.RawMessage
	SubscriptionID string
}

// ServerMessage is a control message for the client (ack, error, pong),
// before protocol encoding.
type ServerMessage struct {
	Type    string
	ID      string
	Channel string
	Payload json.RawMessage
}

// Message is an encoded frame ready to write to a client. Besides the wire
// bytes it carries the routing and logging context of the delivery.
type Message struct {
	Data           []byte
	Kind           websocket.MessageType
	DispatchID     string
	Channel        string
	SubscriptionID string
	Source         string
	ControlType    string
	TypeName       string
	Target         string
	ClientID       string
	Adapter        string
	Subprotocol    string
	Direction      string
}

// Payload is an event body encoded for a protocol: JSON bytes, or
// protobuf bytes with Value holding their base64 form.
type Payload struct {
	Data        []byte
	Kind        websocket.MessageType
	Value       any
	ContentType string
	TypeName    string
}

// ProtobufPayload wraps protobuf wire bytes of typeName as a binary socket
// payload. Value carries the base64 form for adapters that embed it in JSON.
func ProtobufPayload(typeName string, data []byte) Payload {
	return Payload{
		Data:        data,
		Kind:        websocket.MessageBinary,
		Value:       base64.StdEncoding.EncodeToString(data),
		ContentType: "application/x-protobuf",
		TypeName:    typeName,
	}
}

// Greeter is implemented by protocols that acknowledge the connection as
// soon as it opens, without waiting for an init message.
type Greeter interface {
	GreetsOnConnect() bool
}

// Protocol encodes and parses one WebSocket wire protocol.
type Protocol interface {
	ParseClientMessage(b []byte) (ClientMessage, error)
	EncodePayload(payload json.RawMessage) (Payload, error)
	WrapData(payload Payload, subID, channel string) (Message, error)
	EncodeServerMessage(msg ServerMessage) (Message, error)
	Heartbeat() (Message, time.Duration)
	Subprotocols() []string
}
