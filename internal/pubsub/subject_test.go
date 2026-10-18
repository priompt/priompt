package pubsub

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// Distinct prompt URIs must map to distinct subjects. Segments may contain
// dots, which are NATS token separators, so they have to be encoded.
func TestSubjectIsInjective(t *testing.T) {
	for _, pair := range [][2]string{
		{"priompt://acme/a.b", "priompt://acme/a/b"},
		{"priompt://acme/corp/x", "priompt://acme.corp/x"},
		{"priompt://acme/a.b/c", "priompt://acme/a/b.c"},
	} {
		if a, b := Subject(pair[0]), Subject(pair[1]); a == b {
			t.Errorf("%s and %s share subject %q", pair[0], pair[1], a)
		}
	}
	// Dot-free URIs keep their subject.
	if got := Subject("priompt://acme/support/agent-v2_x"); got != "priompt.acme.support.agent-v2_x" {
		t.Errorf("Subject = %q", got)
	}
	if got := Subject("priompt://acme.corp/x/a.b"); got != "priompt.acme%2Ecorp.x.a%2Eb" {
		t.Errorf("Subject = %q", got)
	}
	if got := OrgSubject("acme.corp"); got != "priompt.acme%2Ecorp.>" {
		t.Errorf("OrgSubject = %q", got)
	}
}

// A subscriber scoped to org "acme" must not receive events of org "acme.corp".
func TestOrgPermissionIsExact(t *testing.T) {
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

	nc, err := nats.Connect(bus.ClientURL(), nats.UserInfo("acme", "a"), nats.Timeout(2*time.Second),
		nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	got := make(chan string, 4)
	if _, err := nc.Subscribe("priompt.>", func(m *nats.Msg) { got <- m.Subject }); err != nil {
		t.Fatal(err)
	}
	if _, err := nc.Subscribe(OrgSubject("acme"), func(m *nats.Msg) { got <- m.Subject }); err != nil {
		t.Fatal(err)
	}
	nc.Flush()

	if err := bus.Publish("priompt://acme.corp/x", "v1", "new"); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish("priompt://acme/x", "v2", "new"); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != Subject("priompt://acme/x") {
			t.Fatalf("acme received %q", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("acme did not receive its own event")
	}
	select {
	case s := <-got:
		t.Fatalf("acme also received %q", s)
	case <-time.After(500 * time.Millisecond):
	}
}
