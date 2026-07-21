package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTokens(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tokens.txt")
	content := `# comment
bare
scoped        acme
writer        acme   rw
expiring      acme   2026-12-31   rw
readonly      acme   ro
`
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PRIOMPT_TOKEN", "envadmin")

	tokens := loadTokens(file)

	// PRIOMPT_TOKEN is admin (no org) and may write.
	if tok, ok := tokens["envadmin"]; !ok || tok.Org != "" || !tok.Write {
		t.Errorf("envadmin should be admin+write, got %+v ok=%v", tok, ok)
	}
	// Write is opt-in: a bare or scoped token without `rw` is read-only.
	if tokens["bare"].Write {
		t.Error("bare token must default read-only")
	}
	if tokens["scoped"].Org != "acme" || tokens["scoped"].Write {
		t.Errorf("scoped token: want org=acme read-only, got %+v", tokens["scoped"])
	}
	if !tokens["writer"].Write {
		t.Error("rw token must be writable")
	}
	// Field order is keyword-based: org, expiry, and rw parse regardless of position.
	e := tokens["expiring"]
	if e.Org != "acme" || !e.Write || e.Expires.IsZero() {
		t.Errorf("expiring token: want org=acme write=true expiry set, got %+v", e)
	}
	if tokens["readonly"].Write {
		t.Error("explicit ro must be read-only")
	}
}
