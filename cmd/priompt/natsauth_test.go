package main

import (
	"testing"
	"time"

	"github.com/priompt/auth/authn"
)

// The broker accepts the same credentials as the API, with the same org scope.
func TestNATSAuth(t *testing.T) {
	if natsAuth(nil) != nil {
		t.Fatal("API auth disabled: broker must rely on -nats-token alone")
	}
	check := natsAuth([]authn.Provider{authn.NewStatic(map[string]authn.Token{
		"acme-tok":    {Org: "acme"},
		"admin-tok":   {Write: true},
		"expired-tok": {Org: "acme", Expires: time.Now().Add(-time.Hour)},
	})})
	for tok, want := range map[string]struct {
		org string
		ok  bool
	}{
		"acme-tok":    {"acme", true},
		"admin-tok":   {"", true},
		"expired-tok": {"", false},
		"unknown":     {"", false},
		"":            {"", false},
	} {
		if org, ok := check(tok); org != want.org || ok != want.ok {
			t.Errorf("natsAuth(%q) = (%q, %v), want (%q, %v)", tok, org, ok, want.org, want.ok)
		}
	}
}
