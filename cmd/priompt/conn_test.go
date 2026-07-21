package main

import "testing"

func TestConnURL(t *testing.T) {
	cases := []struct {
		raw, host, token string
	}{
		{"priompt://deadbeef@example.com:8443", "example.com:8443", "deadbeef"},
		{"priompt://example.com:8443", "example.com:8443", ""}, // no token
		{"localhost:8443", "localhost:8443", ""},                 // bare host, no scheme
		{"", "", ""},                                             // unset
	}
	for _, c := range cases {
		t.Setenv("PRIOMPT_URL", c.raw)
		h, tok := connURL()
		if h != c.host || tok != c.token {
			t.Errorf("connURL(%q) = (%q,%q), want (%q,%q)", c.raw, h, tok, c.host, c.token)
		}
	}

	// envHost falls back to localhost when the URL carries no host.
	t.Setenv("PRIOMPT_URL", "")
	if got := envHost(); got != "localhost:8443" {
		t.Errorf("envHost fallback = %q, want localhost:8443", got)
	}
}
