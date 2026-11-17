package main

import "testing"

// watch logs the broker URL; credentials in it must not reach the log.
func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"nats://127.0.0.1:4222":                         "nats://127.0.0.1:4222",
		"nats://s3cr3t@127.0.0.1:4222":                  "nats://***@127.0.0.1:4222",
		"nats://user:pass@host:4222":                    "nats://***@host:4222",
		"nats://a@h1:4222,nats://b:c@h2:4222,nats://h3": "nats://***@h1:4222,nats://***@h2:4222,nats://h3",
		"tls://to@k@en@host:4222":                       "tls://***@host:4222",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// serve logs the embeddings URL at startup; user info and query (where some
// endpoints take the key) must not reach the log.
func TestEmbedName(t *testing.T) {
	for in, want := range map[string]string{
		"": "lexical(offline)",
		"https://embed.example.com/v1/embeddings":                             "m@https://embed.example.com/v1/embeddings",
		"http://127.0.0.1:8080/v1/embeddings":                                 "m@http://127.0.0.1:8080/v1/embeddings",
		"https://user:s3cret@embed.example.com/v1/embeddings?api-key=abc":     "m@https://***@embed.example.com/v1/embeddings?***",
		"https://tok@embed.example.com/v1/embeddings":                         "m@https://***@embed.example.com/v1/embeddings",
		"https://embed.example.com/openai/deployments/d/embeddings?api-key=k": "m@https://embed.example.com/openai/deployments/d/embeddings?***",
		"https://embed.example.com/v1/embeddings#key=abc":                     "m@https://embed.example.com/v1/embeddings",
		"https://u:p@embed.example.com:bad/v1?api-key=abc":                    "m@***",
	} {
		if got := embedName(in, "m"); got != want {
			t.Errorf("embedName(%q) = %q, want %q", in, got, want)
		}
	}
}

// Connect errors can quote the URL; they are redacted with the same function.
func TestRedactURLInError(t *testing.T) {
	in := `parse "nats://s3cr3t@127.0.0.1:bad": invalid port ":bad" after host`
	want := `parse "nats://***@127.0.0.1:bad": invalid port ":bad" after host`
	if got := redactURL(in); got != want {
		t.Errorf("redactURL = %q, want %q", got, want)
	}
}
