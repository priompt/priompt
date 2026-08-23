// Package pubsub is the Phase 4 distribution layer: an embedded NATS server the
// prompt server runs in-process (so it stays a single binary) plus a client to
// publish version-change events. Agents connect to the NATS port and subscribe
// to a prompt's subject to get push notifications; TTL polling (the L1 cache) is
// the pull side. Eventual consistency by design — TTL is the convergence bound.
package pubsub

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"priomptproto/validate"
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
// URI segments are constrained by validate.URI, which rejects the characters
// that are meaningful here — "." (a token separator), "*" and ">" (wildcards)
// and whitespace — so the mapping cannot be used to forge a subject hierarchy
// or turn a publish into a wildcard.
func Subject(uri string) string {
	path := strings.TrimPrefix(uri, validate.Scheme)
	return "priompt." + strings.ReplaceAll(path, "/", ".")
}

// OrgSubject is the wildcard covering every prompt in one org — the unit of
// subscribe permission.
func OrgSubject(org string) string { return "priompt." + org + ".>" }

// Bus is an embedded NATS server with a publishing client attached.
type Bus struct {
	ns *natsd.Server
	nc *nats.Conn
}

// Config is the embedded broker's listen address and access control.
type Config struct {
	Host string
	Port int

	// Token, when set, is required of every client that connects. The gRPC API
	// is protected by bearer tokens; leaving the notification channel open made
	// that moot. Every change event carries a prompt URI (which names the org),
	// a version hash and the semantic verdict, so an unauthenticated subscriber
	// could enumerate every tenant's prompt namespace and watch its release
	// cadence — and, worse, could *publish*: a forged event names an arbitrary
	// version and an arbitrary classification, and the documented agent pattern
	// is "auto-reload unless the verdict is structural". An attacker who picks
	// the classification owns that gate, and an attacker who picks the version
	// can pin agents to a withdrawn prompt.
	//
	// This is the same posture NATS itself expects for anything shared (users,
	// accounts and subject permissions), Kafka's SASL+ACLs, or an authenticated
	// SSE stream on a feature-flag service: the event channel carries the same
	// identity and authorization as the request channel.
	Token string

	// Users, when non-empty, is used instead of Token: per-user credentials with
	// subject permissions, so a token scoped to one org can only subscribe to
	// that org's subjects.
	Users []User

	// Routes are peer NATS servers to cluster with. Each node embeds its own
	// broker, so without routes a publish that lands on one node is never seen
	// by agents connected to another — in an N-node deployment an agent misses
	// (N-1)/N of all notifications. Clustering, or one shared external NATS,
	// is required as soon as there is more than one node.
	Routes      []string
	ClusterName string
	ClusterHost string
	ClusterPort int

	// RouteSecret authenticates cluster peers. Set it whenever the cluster port
	// is reachable by anything you do not control: a route peer receives every
	// message the cluster carries.
	//
	// Know what it does and does not do. nats-server authenticates *route
	// protocol* peers against this secret, but the cluster listener also accepts
	// ordinary client connections and authenticates those as clients — verified:
	// a client presenting the client token connects to the cluster port and
	// receives cluster traffic, while the route secret alone is refused, and the
	// server counts it as a client, not a route. So this secret gates peers; it
	// does not turn the cluster port into a private one. Anyone holding a client
	// credential can use that port as a second client port.
	//
	// The practical rule, which is also NATS's own guidance: the cluster port
	// belongs on a trusted network and must never be internet-facing. Mutual TLS
	// on the cluster listener (Cluster.TLSConfig with TLSMap) is the only real
	// peer authentication; username/password is a speed bump.
	RouteSecret string
}

// routeUser is the fixed account name for cluster peers. Only the secret varies.
const routeUser = "priompt-route"

// User is one broker credential and the org it may watch.
type User struct {
	Name     string
	Password string
	// Org scopes subscriptions to priompt.<org>.>; empty means every subject.
	Org string
	// Publish allows publishing change events. Only the server itself needs
	// this; agents subscribe.
	Publish bool
}

func (c Config) options() *natsd.Options {
	o := &natsd.Options{Host: c.Host, Port: c.Port, NoLog: true, NoSigs: true}
	switch {
	case len(c.Users) > 0:
		for _, u := range c.Users {
			perms := &natsd.Permissions{
				Subscribe: &natsd.SubjectPermission{Allow: []string{"priompt.>"}},
				Publish:   &natsd.SubjectPermission{Deny: []string{">"}},
			}
			if u.Org != "" {
				perms.Subscribe = &natsd.SubjectPermission{Allow: []string{OrgSubject(u.Org)}}
			}
			if u.Publish {
				perms.Publish = &natsd.SubjectPermission{Allow: []string{"priompt.>"}}
			}
			o.Users = append(o.Users, &natsd.User{
				Username: u.Name, Password: u.Password, Permissions: perms,
			})
		}
	case c.Token != "":
		o.Authorization = c.Token
	}
	if len(c.Routes) > 0 || c.ClusterPort > 0 {
		o.Cluster = natsd.ClusterOpts{
			Name: c.ClusterName, Host: c.ClusterHost, Port: c.ClusterPort,
			Username: routeUser, Password: c.RouteSecret,
		}
		if c.RouteSecret == "" {
			// Leave the credential off entirely rather than configuring an empty
			// password, which nats-server treats as "no auth required".
			o.Cluster.Username, o.Cluster.Password = "", ""
		}
		for _, r := range c.Routes {
			u, err := url.Parse(r)
			if err != nil {
				continue
			}
			// Peers are given as plain nats://host:port; carry the credential so
			// the operator states the secret once instead of repeating it into
			// every route URL (where it would also end up in logs).
			if u.User == nil && c.RouteSecret != "" {
				u.User = url.UserPassword(routeUser, c.RouteSecret)
			}
			o.Routes = append(o.Routes, u)
		}
	}
	return o
}

// NewEmbedded starts an in-process NATS server and connects a publisher to it.
func NewEmbedded(cfg Config) (*Bus, error) {
	ns, err := natsd.NewServer(cfg.options())
	if err != nil {
		return nil, err
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		ns.Shutdown()
		return nil, errors.New("embedded NATS not ready")
	}
	nc, err := nats.Connect(ns.ClientURL(), cfg.publisherAuth()...)
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

// publisherAuth is how the in-process publisher authenticates to its own broker.
func (c Config) publisherAuth() []nats.Option {
	for _, u := range c.Users {
		if u.Publish {
			return []nats.Option{nats.UserInfo(u.Name, u.Password)}
		}
	}
	if c.Token != "" {
		return []nats.Option{nats.Token(c.Token)}
	}
	return nil
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

// Connect dials a NATS server, e.g. nats://127.0.0.1:4222. Credentials travel in
// the URL (nats://token@host:4222 or nats://user:pass@host:4222), as NATS
// clients everywhere expect.
func Connect(url string, opts ...nats.Option) (*Subscriber, error) {
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, err
	}
	return &Subscriber{nc: nc}, nil
}

// Flush blocks until the server has registered every subscription made so far.
// Without it a subscribe issued immediately before a publish can miss the event.
func (s *Subscriber) Flush() error { return s.nc.Flush() }

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
