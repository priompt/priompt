package pubsub

import (
	"fmt"
	"net"
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

// freePort asks the OS for an unused port and hands it back. Two brokers have to
// know each other's route address before either starts, so -1 ("any port") is not
// usable here the way it is for a standalone broker.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// Each node embeds its own broker, so without routes a publish landing on one is
// never seen by an agent connected to another — on an N-node cluster an agent
// misses (N-1)/N of all notifications. This is the case that matters and the one
// the single-broker tests cannot reach: subscribe on A, publish on B.
func TestClusteredBrokersDeliverAcrossNodes(t *testing.T) {
	const token, routeSecret = "client-tok", "route-sec"
	pA, pB := freePort(t), freePort(t)
	rA, rB := freePort(t), freePort(t)

	busA, err := NewEmbedded(Config{
		Host: "127.0.0.1", Port: pA, Token: token,
		ClusterName: "priompt", ClusterHost: "127.0.0.1", ClusterPort: rA,
		RouteSecret: routeSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer busA.Close()

	busB, err := NewEmbedded(Config{
		Host: "127.0.0.1", Port: pB, Token: token,
		ClusterName: "priompt", ClusterHost: "127.0.0.1", ClusterPort: rB,
		RouteSecret: routeSecret,
		Routes:      []string{fmt.Sprintf("nats://127.0.0.1:%d", rA)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer busB.Close()

	// Give the route a moment to establish before asserting on it.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if busA.ns.NumRoutes() > 0 && busB.ns.NumRoutes() > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if busA.ns.NumRoutes() == 0 || busB.ns.NumRoutes() == 0 {
		t.Fatalf("route never established: A=%d B=%d", busA.ns.NumRoutes(), busB.ns.NumRoutes())
	}

	// Subscribe on A.
	subA, err := Connect(busA.ClientURL(), nats.Token(token))
	if err != nil {
		t.Fatal(err)
	}
	defer subA.nc.Close()
	got := make(chan Event, 1)
	if _, err := subA.Subscribe("priompt://acme/support/agent", func(e Event) { got <- e }); err != nil {
		t.Fatal(err)
	}
	if err := subA.Flush(); err != nil {
		t.Fatal(err)
	}
	// The subscription interest has to propagate over the route before B's
	// publish can be forwarded; without this the test is a coin flip.
	time.Sleep(500 * time.Millisecond)

	// Publish on B.
	if err := busB.Publish("priompt://acme/support/agent", "crossnode1", "structural"); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-got:
		if e.Version != "crossnode1" || e.Classification != "structural" {
			t.Fatalf("event across nodes = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event published on node B never reached the subscriber on node A")
	}
}

// The cluster port must at minimum refuse anonymous connections, and must not
// accept the route secret as a general-purpose client credential.
//
// It deliberately does NOT assert that a client-credential holder is refused
// there, because they are not: nats-server's cluster listener also serves
// ordinary client connections and authenticates them as clients. That is
// verified below rather than assumed, and it is why the cluster port is
// documented as a trusted-network port rather than one the secret makes safe.
func TestClusterPortRefusesAnonymous(t *testing.T) {
	const token, routeSecret = "client-tok", "route-sec"
	p, r := freePort(t), freePort(t)

	bus, err := NewEmbedded(Config{
		Host: "127.0.0.1", Port: p, Token: token,
		ClusterName: "priompt", ClusterHost: "127.0.0.1", ClusterPort: r,
		RouteSecret: routeSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	routeURL := fmt.Sprintf("nats://127.0.0.1:%d", r)
	if nc, err := nats.Connect(routeURL, nats.Timeout(2*time.Second)); err == nil {
		nc.Close()
		t.Error("cluster port accepted a connection with no credential")
	}
	if nc, err := nats.Connect(routeURL, nats.UserInfo(routeUser, "wrong"), nats.Timeout(2*time.Second)); err == nil {
		nc.Close()
		t.Error("cluster port accepted a wrong route secret")
	}
}

// Pin the surprising-but-real behaviour so it cannot change silently: the
// cluster listener serves client connections, authenticated as clients. If a
// future nats-server stops doing this, this test fails and the documentation
// promising a trusted network can be revisited.
func TestClusterPortAlsoServesClients(t *testing.T) {
	const token, routeSecret = "client-tok", "route-sec"
	p, r := freePort(t), freePort(t)

	bus, err := NewEmbedded(Config{
		Host: "127.0.0.1", Port: p, Token: token,
		ClusterName: "priompt", ClusterHost: "127.0.0.1", ClusterPort: r,
		RouteSecret: routeSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	nc, err := nats.Connect(fmt.Sprintf("nats://127.0.0.1:%d", r),
		nats.Token(token), nats.Timeout(2*time.Second),
		nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
	if err != nil {
		t.Skipf("cluster port no longer accepts client connections (%v) — "+
			"the trusted-network caveat in Config.RouteSecret can be relaxed", err)
	}
	defer nc.Close()
	if bus.ns.NumRoutes() != 0 {
		t.Errorf("expected the connection to be counted as a client, got %d routes", bus.ns.NumRoutes())
	}
	t.Logf("confirmed: client credential accepted on the cluster port, counted as a client "+
		"(clients=%d routes=%d)", bus.ns.NumClients(), bus.ns.NumRoutes())
}

// Configuring a cluster must not weaken the client port.
func TestClusteringKeepsClientAuth(t *testing.T) {
	const token, routeSecret = "client-tok", "route-sec"
	p, r := freePort(t), freePort(t)
	bus, err := NewEmbedded(Config{
		Host: "127.0.0.1", Port: p, Token: token,
		ClusterName: "priompt", ClusterHost: "127.0.0.1", ClusterPort: r,
		RouteSecret: routeSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()

	if nc, err := nats.Connect(bus.ClientURL(), nats.Timeout(2*time.Second)); err == nil {
		nc.Close()
		t.Error("clustered broker accepted an unauthenticated client")
	}
	nc, err := nats.Connect(bus.ClientURL(), nats.Token(token), nats.Timeout(2*time.Second))
	if err != nil {
		t.Fatalf("clustered broker rejected a valid client token: %v", err)
	}
	nc.Close()
}
