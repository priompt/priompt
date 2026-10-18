package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	store "github.com/priompt/db-adapters"
)

func newKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// writePrompt stores one prompt under the given key ("" = plaintext).
func writePrompt(t *testing.T, db, key string) {
	t.Helper()
	t.Setenv("PRIOMPT_ENCRYPTION_KEY", key)
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Put(context.Background(), store.Prompt{URI: "priompt://acme/x/y", Template: "Hello {name}", Slots: []string{"name"}}); err != nil {
		t.Fatal(err)
	}
}

func checkWith(t *testing.T, db, key string) error {
	t.Helper()
	t.Setenv("PRIOMPT_ENCRYPTION_KEY", key)
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	return checkEncryptionKey(st)
}

func TestCheckEncryptionKey(t *testing.T) {
	k1, k2 := newKey(t), newKey(t)
	cases := []struct {
		name, writeKey, readKey, wantErr string
	}{
		{"right key", k1, k1, ""},
		{"missing key", k1, "", "PRIOMPT_ENCRYPTION_KEY is not set"},
		{"wrong key", k1, k2, "does not decrypt"},
		{"plaintext db, no key", "", "", ""},
		{"plaintext db, key set", "", k1, ""}, // turning encryption on later is allowed
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := filepath.Join(t.TempDir(), "p.db")
			writePrompt(t, db, c.writeKey)
			err := checkWith(t, db, c.readKey)
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr) ||
				!strings.Contains(err.Error(), "encryption key missing or wrong")):
				t.Fatalf("err = %v, want one containing %q", err, c.wantErr)
			}
		})
	}

	t.Run("empty db", func(t *testing.T) {
		if err := checkWith(t, filepath.Join(t.TempDir(), "p.db"), k1); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
