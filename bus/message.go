package bus

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"
)

// Kind names a message type, for example "job.status". Register declares
// each Kind once.
type Kind string

// Class sets how the bus keeps the messages of a Kind for a subscriber.
type Class uint8

const (
	// Durable messages are never dropped. A subscriber gets every one of
	// them, in publish order.
	Durable Class = iota + 1
	// Latest messages are coalesced by key. A subscriber gets only the
	// newest message per key.
	Latest
)

// Origin says what caused a message. Host code sets it. Model output never
// sets it.
type Origin string

const (
	// OriginHuman marks a message typed by the user.
	OriginHuman Origin = "human"
	// OriginSystem marks a message written by the host, for example a
	// message that the bus rerouted to the orchestrator.
	OriginSystem Origin = "system"
	// OriginAgent marks a message sent by an agent.
	OriginAgent Origin = "agent"
)

// AddrType is the kind of a recipient or a sender.
type AddrType string

const (
	// AddrUser is the human user. It has no ID.
	AddrUser AddrType = "user"
	// AddrOrchestrator is the main conversation. It has no ID.
	AddrOrchestrator AddrType = "orchestrator"
	// AddrAgent is one agent, identified by ID.
	AddrAgent AddrType = "agent"
	// AddrTUI is the terminal user interface. It has no ID.
	AddrTUI AddrType = "tui"
	// AddrSubtree is a broadcast to the subtree below the agent ID. An
	// empty ID means the whole tree of the orchestrator.
	AddrSubtree AddrType = "subtree"
)

// Addr says who sends a message or who receives it.
type Addr struct {
	// Type is the kind of the address.
	Type AddrType `json:"type"`
	// ID is the agent ID for AddrAgent and AddrSubtree. It is empty otherwise.
	ID string `json:"id,omitempty"`
}

// Message is one record on the bus. Publish sets Seq, At and Payload. A
// consumer reads the payload with Decode.
type Message struct {
	// Seq is bus-wide and grows by one for each message that Publish
	// accepts. It is also the message id.
	Seq uint64 `json:"seq"`
	// At is the time of publish.
	At time.Time `json:"at"`
	// Kind is the message type that Register declared.
	Kind Kind `json:"kind"`
	// From is the sender.
	From Addr `json:"from"`
	// To is the recipient, or the root of a subtree broadcast.
	To Addr `json:"to"`
	// ReplyTo is the Seq of the message this one answers. Zero means none.
	ReplyTo uint64 `json:"reply_to,omitempty"`
	// Origin says what caused the message. Host code sets it.
	Origin Origin `json:"origin"`
	// Payload is the JSON encoding of the payload that Decode reads.
	Payload json.RawMessage `json:"payload"`
}

// kindInfo is what Register stores for one Kind.
type kindInfo struct {
	class Class
	typ   reflect.Type
	// key is a func(T) string, or nil when the Kind has no key.
	key any
}

var (
	kindsMu sync.Mutex
	kinds   = make(map[Kind]kindInfo)
)

// Register declares kind, its class and the type T of its payloads. key
// maps a payload to the key that coalesces Latest messages. It is required
// for Latest and ignored for Durable.
//
// Register panics when the class is unknown, when Latest has no key, or
// when kind is already registered with another class or another type.
// Registering the same kind again with the same class and type does nothing.
func Register[T any](kind Kind, class Class, key func(T) string) {
	if class != Durable && class != Latest {
		panic(fmt.Sprintf("bus: kind %q: unknown class %d", kind, class))
	}
	if class == Latest && key == nil {
		panic(fmt.Sprintf("bus: kind %q: Latest needs a key function", kind))
	}
	info := kindInfo{class: class, typ: reflect.TypeFor[T]()}
	if class == Latest {
		info.key = key
	}

	kindsMu.Lock()
	defer kindsMu.Unlock()
	if old, ok := kinds[kind]; ok {
		if old.class != info.class || old.typ != info.typ {
			panic(fmt.Sprintf("bus: kind %q is already registered with another class or type", kind))
		}
		return
	}
	kinds[kind] = info
}

// lookupKind returns the registration of kind.
func lookupKind(kind Kind) (kindInfo, bool) {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	info, ok := kinds[kind]
	return info, ok
}

// Decode decodes the payload of m into a value of type T.
func Decode[T any](m Message) (T, error) {
	var v T
	if err := json.Unmarshal(m.Payload, &v); err != nil {
		return v, fmt.Errorf("bus: decode %q: %w", m.Kind, err)
	}
	return v, nil
}
