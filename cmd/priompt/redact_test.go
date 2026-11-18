package main

import "testing"

// watch logs the broker URL; credentials in it must not reach the log.
func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"nats://127.0.0.1:4222":                           "nats://127.0.0.1:4222",
		"nats://s3cr3t@127.0.0.1:4222":                    "nats://***@127.0.0.1:4222",
		"nats://user:pass@host:4222":                      "nats://***@host:4222",
		"nats://a@h1:4222,nats://b:***@h2:4222,nats://h3": "nats://***@h1:4222,nats://***@h2:4222,nats://h3",
		"tls://to@k@en@host:4222":                         "tls://***@host:4222",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
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

// redactEmbedURL strips user info and query string from embeddings URLs
// for safe logging.
func TestRedactEmbedURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://embed.example.com/v1/embeddings":                       "https://embed.example.com/v1/embeddings",
		"https://user:pass@embed.example.com/v1/embeddings":             "https://embed.example.com/v1/embeddings",
		"https://user:pass@embed.example.com/v1/embeddings?api-key=abc": "https://embed.example.com/v1/embeddings",
		"https://user@embed.example.com/v1/embeddings?api-key=abc":      "https://embed.example.com/v1/embeddings",
		"https://embed.example.com/v1/embeddings?model=foo&dim=1536":    "https://embed.example.com/v1/embeddings",
		"https://user:pass@host:8080/v1/embeddings":                     "https://host:8080/v1/embeddings",
		"https://user:pass@host:8080/v1/embeddings?key=abc":             "https://host:8080/v1/embeddings",
		// fallback when parsing fails
		"not-a-url":     "not-a-url",
		"https:///bad":  "https:///bad", // no host
		"https://@host": "https://host", // empty user
	} {
		if got := redactEmbedURL(in); got != want {
			t.Errorf("redactEmbedURL(%q) = %q, want %q", in, got, want)
		}
	}
}
