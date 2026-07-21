// Package pubsub is the Phase 4 distribution layer: an embedded NATS server the
// prompt server runs in-process (so it stays a single binary) plus a client to
// publish version-change events. Agents connect to the NATS port and subscribe
// to a prompt's subject to get push notifications; TTL polling (the L1 cache) is
// the pull side. Eventual consistency by design — TTL is the convergence bound.
package pubsub

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Event is a version-change notification. Classification is the semantic diff
// verdict (structural | localized tweak | minor edit | new | ""), so an agent
// can auto-reload a localized tweak but hold a structural change for review.
type Event struct {
	Version        string `json:"version"`
	Classification string `json:"classification,omitempty"`
}

// Subject maps a prompt URI to its NATS subject:
//
//	priompt://acme/support/agent  ->  priompt.acme.support.agent
//
// ponytail: assumes URI path segments are NATS-token-safe (no spaces/dots);
// sanitize here if prompt names ever get exotic.
func Subject(uri string) string {
	path := strings.TrimPrefix(uri, "priompt://")
	return "priompt." + strings.ReplaceAll(path, "/", ".")
}

// Bus is an embedded NATS server with a publishing client attached.
type Bus struct {
	ns *natsd.Server
	nc *nats.Conn
}

// NewEmbedded starts an in-process NATS server on host:port and connects a
// publisher to it.
func NewEmbedded(host string, port int) (*Bus, error) {
	ns, err := natsd.NewServer(&natsd.Options{Host: host, Port: port, NoLog: true, NoSigs: true})
	if err != nil {
		return nil, err
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		ns.Shutdown()
		return nil, errors.New("embedded NATS not ready")
	}
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		ns.Shutdown()
		return nil, err
	}
	return &Bus{ns: ns, nc: nc}, nil
}

// Publish notifies subscribers that the prompt at uri now has versionHash, with
// the semantic diff classification of the change.
func (b *Bus) Publish(uri, versionHash, classification string) error {
	body, err := json.Marshal(Event{Version: versionHash, Classification: classification})
	if err != nil {
		return err
	}
	return b.nc.Publish(Subject(uri), body)
}

// ClientURL is the nats:// address agents connect to.
func (b *Bus) ClientURL() string { return b.ns.ClientURL() }

func (b *Bus) Close() {
	b.nc.Drain()
	b.ns.Shutdown()
}

// Subscriber is the pull-side counterpart to Bus: an agent connects to the NATS
// port the server exposes and gets a callback whenever a prompt it uses changes.
// It hides the subject mapping so embedders never hand-roll NATS subjects.
type Subscriber struct{ nc *nats.Conn }

// Connect dials a NATS server, e.g. nats://127.0.0.1:4222.
func Connect(url string) (*Subscriber, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, err
	}
	return &Subscriber{nc: nc}, nil
}

// Subscribe calls fn(event) on every version change of the prompt at uri.
func (s *Subscriber) Subscribe(uri string, fn func(Event)) (*nats.Subscription, error) {
	return s.nc.Subscribe(Subject(uri), func(m *nats.Msg) { fn(parseEvent(m.Data)) })
}

// parseEvent decodes a notification body; a non-JSON body is treated as a bare
// version hash (back-compat with pre-0.7 publishers).
func parseEvent(b []byte) Event {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil || e.Version == "" {
		return Event{Version: string(b)}
	}
	return e
}

func (s *Subscriber) Close() { s.nc.Drain() }
