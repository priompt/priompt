package pubsub

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// API credentials admitted by Authenticate are subscribe-only and scoped to
// their org; the broker token keeps full rights for the server and existing
// single-token setups.
func TestAPITokensAreScopedSubscribers(t *testing.T) {
	bus, err := NewEmbedded(Config{
		Host: "127.0.0.1", Port: -1, Token: "broker",
		Authenticate: func(tok string) (string, time.Time, bool) {
			switch tok {
			case "acme-tok":
				return "acme", time.Time{}, true
			case "admin-tok":
				return "", time.Time{}, true
			case "short-tok":
				return "acme", time.Now().Add(500 * time.Millisecond), true
			}
			return "", time.Time{}, false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	connect := func(opts ...nats.Option) (*nats.Conn, error) {
		opts = append(opts, nats.Timeout(2*time.Second),
			nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
		return nats.Connect(bus.ClientURL(), opts...)
	}
	for name, opt := range map[string]nats.Option{
		"no credential": nats.Token(""),
		"unknown token": nats.Token("nope"),
	} {
		if nc, err := connect(opt); err == nil {
			nc.Close()
			t.Errorf("%s: connected", name)
		}
	}

	// receive subscribes to both orgs and reports which events arrive.
	receive := func(nc *nats.Conn) (acme, beta chan string) {
		acme, beta = make(chan string, 4), make(chan string, 4)
		nc.Subscribe(Subject("priompt://acme/x/y"), func(m *nats.Msg) { acme <- parseEvent(m.Data).Version })
		nc.Subscribe(Subject("priompt://beta/x/y"), func(m *nats.Msg) { beta <- parseEvent(m.Data).Version })
		nc.Flush()
		return
	}
	expect := func(t *testing.T, ch chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if want == "" {
				t.Fatalf("received %q, want nothing", got)
			}
			if got != want {
				t.Fatalf("received %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			if want != "" {
				t.Fatalf("no event, want %q", want)
			}
		}
	}

	scoped, err := connect(nats.Token("acme-tok"))
	if err != nil {
		t.Fatalf("org token rejected: %v", err)
	}
	defer scoped.Close()
	admin, err := connect(nats.Token("admin-tok"))
	if err != nil {
		t.Fatalf("admin API token rejected: %v", err)
	}
	defer admin.Close()
	broker, err := connect(nats.Token("broker"))
	if err != nil {
		t.Fatalf("broker token rejected: %v", err)
	}
	defer broker.Close()

	sAcme, sBeta := receive(scoped)
	aAcme, aBeta := receive(admin)
	bAcme, _ := receive(broker)

	if err := bus.Publish("priompt://acme/x/y", "v1", "new"); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish("priompt://beta/x/y", "v2", "new"); err != nil {
		t.Fatal(err)
	}
	expect(t, sAcme, "v1")
	expect(t, sBeta, "") // another org's events are not delivered
	expect(t, aAcme, "v1")
	expect(t, aBeta, "v2")
	expect(t, bAcme, "v1")

	// API credentials cannot publish (forge) events; the broker token can.
	scoped.Publish(Subject("priompt://acme/x/y"), []byte("forged-scoped"))
	scoped.Flush()
	admin.Publish(Subject("priompt://acme/x/y"), []byte("forged-admin"))
	admin.Flush()
	expect(t, bAcme, "")
	broker.Publish(Subject("priompt://acme/x/y"), []byte("from-broker"))
	broker.Flush()
	expect(t, bAcme, "from-broker")

	// A credential's expiry closes its connection: the subscription must not
	// outlive the token.
	closed := make(chan struct{})
	short, err := connect(nats.Token("short-tok"), nats.NoReconnect(),
		nats.ClosedHandler(func(*nats.Conn) { close(closed) }))
	if err != nil {
		t.Fatalf("short-lived token rejected: %v", err)
	}
	defer short.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("connection still open after its token expired")
	}
	if scoped.IsClosed() {
		t.Fatal("a token without expiry was disconnected")
	}
}
