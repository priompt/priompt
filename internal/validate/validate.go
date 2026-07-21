// Package validate is the ACID "consistency" guarantee: a prompt that fails
// here never enters a valid state (rejected at commit, never served).
package validate

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Slots are written as {name} in the template.
var placeholderRe = regexp.MustCompile(`\{(\w+)\}`)

// Prompt checks the template is well-formed and that declared slots exactly
// match the {placeholders} used in the template — no undeclared, no unused.
func Prompt(uri, template string, slots []string) error {
	if strings.TrimSpace(uri) == "" {
		return errors.New("uri is empty")
	}
	if strings.TrimSpace(template) == "" {
		return errors.New("template is empty")
	}

	found := map[string]bool{}
	for _, m := range placeholderRe.FindAllStringSubmatch(template, -1) {
		found[m[1]] = true
	}
	declared := map[string]bool{}
	for _, s := range slots {
		if strings.TrimSpace(s) == "" {
			return errors.New("slot name is empty")
		}
		declared[s] = true
	}

	var missing, extra []string
	for s := range found {
		if !declared[s] {
			missing = append(missing, s)
		}
	}
	for s := range declared {
		if !found[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		return fmt.Errorf("template uses undeclared slots: %s", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return fmt.Errorf("declared slots never used in template: %s", strings.Join(extra, ", "))
	}
	return nil
}
