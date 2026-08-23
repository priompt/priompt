package pubsub

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// The broker must refuse an unauthenticated client once a token is configured.
// An open broker leaks every org's prompt addresses, version hashes and change
// cadence to anyone who can reach the port — and lets them publish forged events
// whose classification field is exactly what agents gate auto-reload on.
func TestTokenRequired(t *testing.T) {
	bus, err := NewEmbedded(Config{Host: "127.0.0.1", Port: -1, Token: "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	if _, err := nats.Connect(bus.ClientURL(), nats.Timeout(2*time.Second)); err == nil {
		t.Fatal("connected with no credentials; the broker is open")
	}
	if _, err := nats.Connect(bus.ClientURL(), nats.Token("wrong"), nats.Timeout(2*time.Second)); err == nil {
		t.Fatal("connected with the wrong token")
	}

	nc, err := nats.Connect(bus.ClientURL(), nats.Token("s3cr3t"), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("correct token rejected: %v", err)
	}
	defer nc.Close()
}

// The server's own publisher must still reach its broker when a token is set.
func TestPublishWithToken(t *testing.T) {
	bus, err := NewEmbedded(Config{Host: "127.0.0.1", Port: -1, Token: "s3cr3t"})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	sub, err := Connect(bus.ClientURL(), nats.Token("s3cr3t"))
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan Event, 1)
	if _, err := sub.Subscribe("priompt://acme/support/agent", func(e Event) { got <- e }); err != nil {
		t.Fatal(err)
	}
	if err := sub.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish("priompt://acme/support/agent", "abc123", "localized tweak"); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-got:
		if e.Version != "abc123" || e.Classification != "localized tweak" {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event delivered")
	}
}

// Per-user credentials scope a subscriber to its own org's subjects, so one
// tenant cannot watch another's prompts even with a valid credential.
func TestSubjectPermissionsScopeByOrg(t *testing.T) {
	bus, err := NewEmbedded(Config{
		Host: "127.0.0.1", Port: -1,
		Users: []User{
			{Name: "server", Password: "pub", Publish: true},
			{Name: "acme", Password: "a", Org: "acme"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	nc, err := nats.Connect(bus.ClientURL(), nats.UserInfo("acme", "a"), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	errs := make(chan error, 4)
	nc.SetErrorHandler(func(*nats.Conn, *nats.Subscription, error) { errs <- nil })

	own := make(chan Event, 1)
	other := make(chan Event, 1)
	if _, err := nc.Subscribe(Subject("priompt://acme/x/y"), func(*nats.Msg) { own <- Event{} }); err != nil {
		t.Fatal(err)
	}
	// Permitted at the client, refused by the server.
	if _, err := nc.Subscribe(Subject("priompt://beta/x/y"), func(*nats.Msg) { other <- Event{} }); err != nil {
		t.Fatal(err)
	}
	nc.Flush()

	if err := bus.Publish("priompt://acme/x/y", "v1", "new"); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish("priompt://beta/x/y", "v1", "new"); err != nil {
		t.Fatal(err)
	}

	select {
	case <-own:
	case <-time.After(3 * time.Second):
		t.Fatal("acme did not receive its own org's event")
	}
	select {
	case <-other:
		t.Fatal("acme received beta's event; subject permissions are not enforced")
	case <-time.After(500 * time.Millisecond):
	}
}

// Subject mapping must not let a URI forge extra hierarchy or a wildcard.
// validate.URI is what guarantees this, so keep them honest together.
func TestSubjectMapping(t *testing.T) {
	if got := Subject("priompt://acme/support/agent"); got != "priompt.acme.support.agent" {
		t.Errorf("Subject = %q", got)
	}
	if got := OrgSubject("acme"); got != "priompt.acme.>" {
		t.Errorf("OrgSubject = %q", got)
	}
}
