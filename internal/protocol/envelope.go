// Package protocol defines the message envelope and payload types for
// communication between guest VMs and the host agent via shed's plugin bus.
//
// These types are defined locally rather than importing from
// github.com/charliek/shed/internal/plugin to avoid pulling shed's
// dependency tree. The JSON wire format is the contract.
package protocol

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// MessageType identifies the role of a message in a conversation.
type MessageType string

const (
	MessageTypeRequest  MessageType = "request"
	MessageTypeResponse MessageType = "response"
	MessageTypeEvent    MessageType = "event"
)

// Envelope is the universal message format for all plugin communication.
type Envelope struct {
	ID        string          `json:"id"`
	Namespace string          `json:"namespace"`
	Type      MessageType     `json:"type"`
	InReplyTo string          `json:"in_reply_to,omitempty"`
	Final     bool            `json:"final"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
	Shed      *ShedInfo       `json:"shed,omitempty"`
}

// ShedInfo identifies the shed instance that originated or is targeted by a message.
type ShedInfo struct {
	Name    string `json:"name"`
	Backend string `json:"backend"`
	Server  string `json:"server"`
}

// NewEnvelope creates a new envelope with a UUIDv7 ID and current timestamp.
func NewEnvelope(namespace string, msgType MessageType, payload json.RawMessage) *Envelope {
	return &Envelope{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Namespace: namespace,
		Type:      msgType,
		Final:     true,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	}
}

// NewResponse creates a response envelope linked to an original request.
func NewResponse(inReplyTo, namespace string, payload json.RawMessage) *Envelope {
	return &Envelope{
		ID:        uuid.Must(uuid.NewV7()).String(),
		Namespace: namespace,
		Type:      MessageTypeResponse,
		InReplyTo: inReplyTo,
		Final:     true,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	}
}
