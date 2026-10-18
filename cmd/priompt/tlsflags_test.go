package main

import "testing"

// A half-configured TLS setup must fail at startup instead of serving
// plaintext while the startup line reports tls=true.
func TestCheckTLSFlags(t *testing.T) {
	for _, c := range []struct {
		cert, key, ca string
		ok            bool
	}{
		{"", "", "", true},
		{"c.pem", "k.pem", "", true},
		{"c.pem", "k.pem", "ca.pem", true},
		{"c.pem", "", "", false},
		{"", "k.pem", "", false},
		{"", "", "ca.pem", false},
		{"c.pem", "", "ca.pem", false},
	} {
		if err := checkTLSFlags(c.cert, c.key, c.ca); (err == nil) != c.ok {
			t.Errorf("checkTLSFlags(%q, %q, %q) = %v, want ok=%v", c.cert, c.key, c.ca, err, c.ok)
		}
	}
}
