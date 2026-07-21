package validate

import "testing"

func TestPrompt(t *testing.T) {
	ok := func(uri, tmpl string, slots []string) {
		if err := Prompt(uri, tmpl, slots); err != nil {
			t.Errorf("want valid, got %v", err)
		}
	}
	bad := func(uri, tmpl string, slots []string) {
		if err := Prompt(uri, tmpl, slots); err == nil {
			t.Errorf("want error for %q", tmpl)
		}
	}

	ok("priompt://o/r/p", "Hello {name}, welcome to {org}.", []string{"name", "org"})
	ok("priompt://o/r/p", "No slots here.", nil)
	bad("", "x", nil)                                      // empty uri
	bad("priompt://o/r/p", "  ", nil)                    // empty template
	bad("priompt://o/r/p", "Hi {name}", nil)            // undeclared slot
	bad("priompt://o/r/p", "Hi there", []string{"name"}) // unused slot
	bad("priompt://o/r/p", "Hi {name}", []string{""})   // empty slot name
}
