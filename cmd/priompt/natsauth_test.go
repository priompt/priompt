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
	exp := time.Now().Add(time.Hour)
	check := natsAuth([]authn.Provider{authn.NewStatic(map[string]authn.Token{
		"acme-tok":    {Org: "acme", Expires: exp},
		"admin-tok":   {Write: true},
		"expired-tok": {Org: "acme", Expires: time.Now().Add(-time.Hour)},
	})})
	for tok, want := range map[string]struct {
		org string
		exp time.Time
		ok  bool
	}{
		"acme-tok":    {"acme", exp, true},
		"admin-tok":   {"", time.Time{}, true},
		"expired-tok": {"", time.Time{}, false},
		"unknown":     {"", time.Time{}, false},
		"":            {"", time.Time{}, false},
	} {
		if org, e, ok := check(tok); org != want.org || !e.Equal(want.exp) || ok != want.ok {
			t.Errorf("natsAuth(%q) = (%q, %v, %v), want (%q, %v, %v)", tok, org, e, ok, want.org, want.exp, want.ok)
		}
	}
}
