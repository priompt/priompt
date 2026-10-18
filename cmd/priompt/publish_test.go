package main

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	store "github.com/priompt/db-adapters"
	"github.com/priompt/priompt/internal/server"
	pb "github.com/priompt/proto/gen/priompt/v1"
)

type countNotifier struct{ n int }

func (c *countNotifier) Publish(string, string, string) error { c.n++; return nil }

// `priompt publish` claims "subscribers notified" only when the server sent a
// change event; an identical republish is a no-op and must say so.
func TestPublishReportsNotification(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	notes := &countNotifier{}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterPromptServiceServer(gs, server.NewServer(st, nil, nil, notes))
	go gs.Serve(lis)
	defer gs.Stop()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := pb.NewPromptServiceClient(conn)
	req := &pb.PublishPromptRequest{Uri: "priompt://o/r/p", Template: "Hi {name}", Slots: []string{"name"}}

	for i, want := range []struct {
		notified bool
		events   int
	}{{true, 1}, {false, 1}} {
		line, err := publishPrompt(context.Background(), c, req)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(line, "subscribers notified"); got != want.notified || notes.n != want.events {
			t.Errorf("publish %d: %q with %d events; want notified=%v, %d events", i+1, line, notes.n, want.notified, want.events)
		}
	}
}
