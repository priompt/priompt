package main

import "testing"

func TestSetEnvLine(t *testing.T) {
	const url = "priompt://deadbeef@localhost:8443"
	cases := map[string]string{
		"windows": `$env:PRIOMPT_URL = "priompt://deadbeef@localhost:8443"`,
		"linux":   "export PRIOMPT_URL=priompt://deadbeef@localhost:8443",
		"darwin":  "export PRIOMPT_URL=priompt://deadbeef@localhost:8443",
	}
	for goos, want := range cases {
		if got := setEnvLine(goos, "PRIOMPT_URL", url); got != want {
			t.Errorf("setEnvLine(%q) = %q, want %q", goos, got, want)
		}
	}
}
