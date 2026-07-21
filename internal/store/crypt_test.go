package store

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptionRoundTrip(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32)) // all-zero 32-byte key
	t.Setenv("PRIOMPT_ENCRYPTION_KEY", key)

	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "enc.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	const uri, tmpl = "priompt://acme/x", "secret {name}"
	if _, err := st.Commit(ctx, uri, DefaultBranch, tmpl, []string{"name"}, "me", "msg"); err != nil {
		t.Fatal(err)
	}

	// Read back through the store -> plaintext.
	got, err := st.Get(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if got.Template != tmpl || len(got.Slots) != 1 || got.Slots[0] != "name" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	// Raw column bytes must be ciphertext, not the plaintext template.
	var rawT, rawS string
	if err := st.db.QueryRow(`SELECT template, slots FROM prompts WHERE uri=?`, uri).Scan(&rawT, &rawS); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawT, encPrefix) || strings.Contains(rawT, "secret") {
		t.Errorf("template stored in the clear: %q", rawT)
	}
	if !strings.HasPrefix(rawS, encPrefix) || strings.Contains(rawS, "name") {
		t.Errorf("slots stored in the clear: %q", rawS)
	}
}

func TestPlaintextRowReadableWithKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "mix.db")

	// Write a row with NO key configured (plaintext, no marker).
	plain, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const uri = "priompt://acme/legacy"
	if _, err := plain.Commit(ctx, uri, DefaultBranch, "old {x}", []string{"x"}, "me", "m"); err != nil {
		t.Fatal(err)
	}
	plain.Close()

	// Reopen WITH a key: the unmarked legacy row must still read back.
	t.Setenv("PRIOMPT_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	enc, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	got, err := enc.Get(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if got.Template != "old {x}" {
		t.Fatalf("legacy plaintext row unreadable: %+v", got)
	}
}
