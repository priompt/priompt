package main

import "testing"

// The JWKS keys decide which JWTs the server trusts; plain HTTP to a
// non-loopback host is warned about, everything else is silent.
func TestInsecureJWKSURL(t *testing.T) {
	for in, want := range map[string]bool{
		"http://auth.internal:8444/jwks": true,
		"http://10.0.0.5:8444/jwks":      true,
		"https://auth.internal/jwks":     false,
		"http://127.0.0.1:8444/jwks":     false,
		"http://localhost:8444/jwks":     false,
		"http://[::1]:8444/jwks":         false,
	} {
		if got := insecureJWKSURL(in); got != want {
			t.Errorf("insecureJWKSURL(%q) = %v, want %v", in, got, want)
		}
	}
}
