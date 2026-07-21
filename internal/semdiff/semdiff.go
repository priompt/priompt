// Package semdiff implements Semantic Propagation Diff: instead of flagging a
// prompt edit as a flat text change, it measures how far the meaning shift
// ripples through surrounding context.
//
//	Signal 1  the changed region (a line-level diff hunk)
//	Signal 2  semantic delta at the point of change (1 - cosine of old vs new)
//	Signal 3  propagation profile — expand the window outward (±2, ±4, ±6 …),
//	          up and down independently, recomputing the delta at each window,
//	          until the curve flattens or hits the file boundary.
//
// A high Signal 2 with a flat Signal 3 is a localized tweak. A Signal 3 that
// stays high out to the boundary is structural — it is reshaping how the
// surrounding instructions relate, the dangerous kind in production.
//
// The novel part (propagation + adaptive windowing + classification) lives
// here. Embeddings are a swappable primitive behind Embedder.
package semdiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"os"
	"strings"
	"unicode"
)

// EmbedderFromEnv returns the embedder configured via PRIOMPT_EMBED_URL /
// _MODEL / _KEY, falling back to the offline lexical embedder when no URL is set.
func EmbedderFromEnv() Embedder {
	if url := os.Getenv("PRIOMPT_EMBED_URL"); url != "" {
		return HTTPEmbedder{URL: url, Model: os.Getenv("PRIOMPT_EMBED_MODEL"), APIKey: os.Getenv("PRIOMPT_EMBED_KEY")}
	}
	return LexicalEmbedder{}
}

// Tunables. Heuristics — ponytail: tune on real prompt-edit data, these are
// reasonable defaults, not measured constants.
const (
	flattenEps     = 0.02 // |Δ between successive windows| below this = flattened
	localThresh    = 0.15 // point delta at/above this is a real behavioral change
	structMinDelta = 0.15 // a direction must still carry this much at the boundary
	persistFrac    = 0.60 // …and at least this fraction of the point delta (default)
)

// Calibrated lets an embedder lower the structural persistence bar. The fraction
// above assumes a semantic embedder, whose meaning shift stays high as the window
// grows. A lexical bag-of-words distance instead *dilutes* toward zero as shared
// context enters the window, so a genuine mid-prompt rewrite lands well under 0.60
// at the boundary and would never read as structural. Such embedders implement
// this to report a lower fraction; persistFracFor falls back to persistFrac.
type Calibrated interface{ PersistFrac() float64 }

func persistFracFor(emb Embedder) float64 {
	if c, ok := emb.(Calibrated); ok {
		return c.PersistFrac()
	}
	return persistFrac
}

// Embedder maps texts to vectors. Implementations: LexicalEmbedder (offline
// default) and HTTPEmbedder (any OpenAI-compatible endpoint).
type Embedder interface {
	Embed(texts []string) ([][]float32, error)
}

// Change is one diff hunk: old[OldStart:OldEnd] became new[NewStart:NewEnd].
// Half-open ranges; an empty old range is an insertion, empty new is a deletion.
type Change struct {
	OldStart, OldEnd int
	NewStart, NewEnd int
}

type Window struct {
	Radius int
	Delta  float64
}

type Direction struct {
	Curve             []Window
	StoppedAtBoundary bool // hit the file edge before the curve flattened
}

type Result struct {
	Change  Change
	Signal2 float64
	Up      Direction
	Down    Direction
	Class   string // "structural" | "localized tweak" | "minor edit"
}

// Analyze runs the full propagation diff over two versions of a prompt.
func Analyze(emb Embedder, oldLines, newLines []string) ([]Result, error) {
	cache := map[string][]float32{} // text -> vector, reused across windows
	pf := persistFracFor(emb)
	var out []Result
	for _, ch := range changes(oldLines, newLines) {
		r, err := analyzeChange(emb, oldLines, newLines, ch, cache, pf)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func analyzeChange(emb Embedder, old, new []string, ch Change, cache map[string][]float32, pf float64) (Result, error) {
	core := func() (string, string) {
		return join(old[ch.OldStart:ch.OldEnd]), join(new[ch.NewStart:ch.NewEnd])
	}
	oc, nc := core()
	s2, err := deltaText(emb, oc, nc, cache)
	if err != nil {
		return Result{}, err
	}

	// A window of radius r is the changed region grown by r lines toward the
	// edge, taken from both files so nearby *other* changes stay inside it.
	up, err := expand(s2, func(r int) (string, string, bool) {
		oS, nS := max(0, ch.OldStart-r), max(0, ch.NewStart-r)
		return join(old[oS:ch.OldEnd]), join(new[nS:ch.NewEnd]), oS == 0 && nS == 0
	}, emb, cache)
	if err != nil {
		return Result{}, err
	}
	down, err := expand(s2, func(r int) (string, string, bool) {
		oE, nE := min(len(old), ch.OldEnd+r), min(len(new), ch.NewEnd+r)
		return join(old[ch.OldStart:oE]), join(new[ch.NewStart:nE]), oE == len(old) && nE == len(new)
	}, emb, cache)
	if err != nil {
		return Result{}, err
	}

	return Result{Change: ch, Signal2: s2, Up: up, Down: down, Class: classify(s2, up, down, pf)}, nil
}

// window returns (oldText, newText, atBoundary) for a given radius.
type window func(r int) (string, string, bool)

func expand(s2 float64, w window, emb Embedder, cache map[string][]float32) (Direction, error) {
	prev := s2
	var d Direction
	for r := 2; ; r += 2 {
		oldT, newT, atBoundary := w(r)
		delta, err := deltaText(emb, oldT, newT, cache)
		if err != nil {
			return d, err
		}
		d.Curve = append(d.Curve, Window{Radius: r, Delta: delta})
		if math.Abs(delta-prev) < flattenEps { // curve flattened: propagation stopped
			return d, nil
		}
		prev = delta
		if atBoundary { // never flattened, hit the hard cap
			d.StoppedAtBoundary = true
			return d, nil
		}
	}
}

// Worst returns the most significant classification across all hunks —
// structural > localized tweak > minor edit. "" for no changes. This is the
// single verdict to attach to a change notification.
func Worst(results []Result) string {
	rank := map[string]int{"minor edit": 1, "localized tweak": 2, "structural": 3}
	worst := ""
	for _, r := range results {
		if rank[r.Class] > rank[worst] {
			worst = r.Class
		}
	}
	return worst
}

func classify(s2 float64, up, down Direction, pf float64) string {
	persists := func(d Direction) bool {
		if !d.StoppedAtBoundary || len(d.Curve) == 0 {
			return false
		}
		last := d.Curve[len(d.Curve)-1].Delta
		return last >= structMinDelta && last >= s2*pf
	}
	if persists(up) || persists(down) {
		return "structural"
	}
	if s2 >= localThresh {
		return "localized tweak"
	}
	return "minor edit"
}

// --- line diff (LCS) -------------------------------------------------------

// changes returns the diff hunks between two line slices.
// ponytail: O(n*m) LCS — fine for prompt-sized files, not for huge ones.
func changes(old, new []string) []Change {
	n, m := len(old), len(new)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if old[i] == new[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	var out []Change
	i, j := 0, 0
	for i < n && j < m {
		if old[i] == new[j] {
			i, j = i+1, j+1
			continue
		}
		oS, nS := i, j
		for i < n && j < m && old[i] != new[j] {
			if dp[i+1][j] >= dp[i][j+1] {
				i++
			} else {
				j++
			}
		}
		out = append(out, Change{oS, i, nS, j})
	}
	if i < n || j < m { // trailing insert or delete
		out = append(out, Change{i, n, j, m})
	}
	return out
}

// --- semantics -------------------------------------------------------------

func deltaText(emb Embedder, a, b string, cache map[string][]float32) (float64, error) {
	if a == b { // identical context contributes no shift, and no embed call
		return 0, nil
	}
	va, err := embedOne(emb, a, cache)
	if err != nil {
		return 0, err
	}
	vb, err := embedOne(emb, b, cache)
	if err != nil {
		return 0, err
	}
	d := 1 - cosine(va, vb)
	return math.Max(0, math.Min(1, d)), nil // clamp; opposite-meaning vectors cap at 1
}

func embedOne(emb Embedder, t string, cache map[string][]float32) ([]float32, error) {
	if v, ok := cache[t]; ok {
		return v, nil
	}
	// ponytail: one text per call; batch via Embed's slice if API round-trips dominate.
	vs, err := emb.Embed([]string{t})
	if err != nil {
		return nil, err
	}
	cache[t] = vs[0]
	return vs[0], nil
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func join(lines []string) string { return strings.Join(lines, "\n") }

// --- embedders -------------------------------------------------------------

// LexicalEmbedder is a hashed bag-of-words embedding: offline, deterministic,
// zero dependencies. It captures lexical overlap, NOT meaning — identical text
// scores 0 and rewordings score low, which is enough to exercise and demo the
// propagation mechanics. For real semantic quality, use HTTPEmbedder.
type LexicalEmbedder struct{ Dim int }

// PersistFrac lowers the structural bar for the offline embedder. Bag-of-words
// distance dilutes as the window grows into shared context: a real mid-prompt
// rewrite survives the boundary at ~0.43 of the point delta, an isolated tweak at
// ~0.37. 0.40 splits them where the default 0.60 cannot. ponytail: the margin is
// thin (it's a toy embedder) — for robust separation use HTTPEmbedder.
func (LexicalEmbedder) PersistFrac() float64 { return 0.40 }

func (e LexicalEmbedder) Embed(texts []string) ([][]float32, error) {
	d := e.Dim
	if d == 0 {
		d = 256
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, d)
		for _, tok := range strings.FieldsFunc(strings.ToLower(t), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			h := fnv.New32a()
			h.Write([]byte(tok))
			v[h.Sum32()%uint32(d)]++
		}
		out[i] = v
	}
	return out, nil
}

// HTTPEmbedder calls any OpenAI-compatible /v1/embeddings endpoint (Ollama,
// text-embedding-inference, llama.cpp, OpenAI itself). Self-hosters point it at
// their own model — the prompts never leave their infrastructure.
type HTTPEmbedder struct {
	URL, Model, APIKey string
}

func (e HTTPEmbedder) Embed(texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]any{"model": e.Model, "input": texts})
	req, err := http.NewRequest("POST", e.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings endpoint: %s", resp.Status)
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	vecs := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		vecs[i] = d.Embedding
	}
	return vecs, nil
}

// --- formatting ------------------------------------------------------------

// Format renders the results for the CLI. Line numbers are 1-based.
func Format(results []Result) string {
	if len(results) == 0 {
		return "no changes\n"
	}
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "change @ new lines %d-%d (old %d-%d): %s\n",
			r.Change.NewStart+1, r.Change.NewEnd, r.Change.OldStart+1, r.Change.OldEnd, Kind(r.Change))
		fmt.Fprintf(&b, "  Signal 2 (point delta): %.3f\n", r.Signal2)
		fmt.Fprintf(&b, "  Signal 3 up:   %s\n", curve(r.Up))
		fmt.Fprintf(&b, "  Signal 3 down: %s\n", curve(r.Down))
		fmt.Fprintf(&b, "  => %s\n", r.Class)
	}
	return b.String()
}

func curve(d Direction) string {
	if len(d.Curve) == 0 {
		return "(none)"
	}
	parts := make([]string, len(d.Curve))
	for i, w := range d.Curve {
		parts[i] = fmt.Sprintf("±%d=%.3f", w.Radius, w.Delta)
	}
	stop := "flat"
	if d.StoppedAtBoundary {
		stop = "boundary"
	}
	return strings.Join(parts, " ") + " (" + stop + ")"
}

// Kind labels a hunk as replace, insert, or delete.
func Kind(c Change) string {
	switch {
	case c.OldStart == c.OldEnd:
		return "insert"
	case c.NewStart == c.NewEnd:
		return "delete"
	default:
		return "replace"
	}
}
