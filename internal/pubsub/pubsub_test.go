package pubsub

import (
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestSubject(t *testing.T) {
	if got := Subject("priompt://acme/support/agent"); got != "priompt.acme.support.agent" {
		t.Errorf("Subject = %q", got)
	}
}

func TestPublishReceive(t *testing.T) {
	bus, err := NewEmbedded("127.0.0.1", -1) // -1 => any free port
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	nc, err := nats.Connect(bus.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	got := make(chan string, 1)
	if _, err := nc.Subscribe(Subject("priompt://o/r/p"), func(m *nats.Msg) {
		got <- string(m.Data)
	}); err != nil {
		t.Fatal(err)
	}
	nc.Flush() // ensure the subscription is registered before publishing

	if err := bus.Publish("priompt://o/r/p", "deadbeef", "structural"); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		// body is JSON carrying both the version and the classification
		if !strings.Contains(v, "deadbeef") || !strings.Contains(v, "structural") {
			t.Errorf("got body %q, want version+classification", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no notification received")
	}
}

func TestSubscriberHelper(t *testing.T) {
	bus, err := NewEmbedded("127.0.0.1", -1)
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	sub, err := Connect(bus.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	got := make(chan Event, 1)
	if _, err := sub.Subscribe("priompt://o/r/p", func(e Event) { got <- e }); err != nil {
		t.Fatal(err)
	}
	sub.nc.Flush()

	if err := bus.Publish("priompt://o/r/p", "cafe123", "localized tweak"); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v.Version != "cafe123" || v.Classification != "localized tweak" {
			t.Errorf("got %+v, want {cafe123, localized tweak}", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber got no notification")
	}
}
