package memory

// Per-entry review of a staged KB snapshot.
//
// `kb approve` is all-or-nothing: it promotes the staged snapshot verbatim. That
// is the right shape for a CLI reviewer reading `kb diff`, but a night that
// distills 160 facts is not usefully judged as one yes/no — one bad fact forces
// rejecting 159 good ones and waiting for the next cycle.
//
// So this file adds a middle step. Review() reports the staged tree as a list of
// changed files, and for the generated `facts/*.md` files it splits each one into
// the individually approvable facts the renderer wrote `<!-- fact-id:N -->`
// markers for. ApplyReview() then RE-AGGREGATES: it rebuilds every affected file
// from the entries that were approved, content-addresses the result, and stages
// THAT as the new pending snapshot.
//
// Two invariants are deliberately preserved rather than worked around:
//
//   - Every snapshot still goes through writeSnapshot, so a digest always names
//     the bytes stored under it. A partial approval produces a NEW digest; it
//     never edits a snapshot in place.
//   - `current` still moves only via Approve(). Per-entry review re-stages; it
//     does not publish, unless the caller explicitly asks (Publish).
//
// Rejection is only durable if the source row is also demoted — otherwise the
// next cycle re-renders the identical fact. ApplyReview reports the rejected
// fact ids so the caller can do that in the memory DB; see the HTTP handler.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// reviewDiffLimit caps the bytes we render an inline diff for. The KB carries a
// ~45MB human-seeded `data/` corpus; a reviewer needs to SEE that such a file
// changed, but shipping its full diff to a browser is neither useful nor cheap.
const reviewDiffLimit = 256 * 1024

// derivedIndexPath is regenerated from whatever survives review rather than
// being approved on its own: it is a table of contents, so a reviewer accepting
// or rejecting it independently of the files it indexes can only produce a
// snapshot whose index lies.
const derivedIndexPath = "facts/INDEX.md"

// ReviewEntry is one independently approvable fact inside a staged facts file.
type ReviewEntry struct {
	FactID string `json:"fact_id"`
	Scope  string `json:"scope"`
	// Body is the rendered markdown for this fact, exactly as it appears in the
	// staged file (minus the id marker).
	Body string `json:"body"`
}

// ReviewLine is one line of a unified diff, pre-classified for the UI.
type ReviewLine struct {
	Kind string `json:"kind"` // "add" | "del" | "ctx" | "hunk"
	Text string `json:"text"`
}

// ReviewFile is one changed path in the staged snapshot.
type ReviewFile struct {
	Path   string `json:"path"`
	Change string `json:"change"` // "added" | "removed" | "modified"
	// PerEntry marks a file whose contents are judged fact-by-fact (Entries)
	// rather than as a whole (Diff).
	PerEntry bool `json:"per_entry"`
	// Derived marks a file the aggregator regenerates, which therefore carries no
	// decision of its own.
	Derived bool `json:"derived"`
	// Entries are the NEW facts this file introduces — the ones under review.
	// Facts already live in `current` are not repeated here.
	Entries []ReviewEntry `json:"entries,omitempty"`
	Diff    []ReviewLine  `json:"diff,omitempty"`
	// Oversized reports that Diff was suppressed because the file exceeds
	// reviewDiffLimit; Bytes then carries the staged size.
	Oversized bool  `json:"oversized,omitempty"`
	Bytes     int64 `json:"bytes,omitempty"`
	// Content is the staged file text (or the live text when the path is being
	// removed) so the review UI can show the whole file next to per-entry
	// decisions. Omitted when the file exceeds reviewDiffLimit.
	Content string `json:"content,omitempty"`
}

// Review is the whole staged snapshot, decomposed for per-entry approval.
type Review struct {
	Current string       `json:"current"`
	Pending *Pending     `json:"pending"`
	Files   []ReviewFile `json:"files"`
	// NewFacts counts the individually approvable entries across all files, so a
	// caller can show progress without walking Files.
	NewFacts int `json:"new_facts"`
}

// Review decomposes the pending snapshot against the live one. It returns a nil
// Review (no error) when nothing is staged.
func (k *KB) Review() (*Review, error) {
	if k == nil {
		return nil, fmt.Errorf("kb: disabled (no root configured)")
	}
	pending, err := k.PendingInfo()
	if err != nil {
		return nil, err
	}
	if pending == nil {
		return nil, nil
	}
	current := k.CurrentDigest()
	before, err := readTree(k.SnapshotDir(current))
	if err != nil {
		return nil, fmt.Errorf("kb: read current snapshot: %w", err)
	}
	after, err := readTree(k.SnapshotDir(pending.Digest))
	if err != nil {
		return nil, fmt.Errorf("kb: read pending snapshot: %w", err)
	}

	out := &Review{Current: current, Pending: pending}
	for _, rel := range changedPaths(before, after) {
		oldText, hadOld := before[rel]
		newText, hasNew := after[rel]
		file := ReviewFile{Path: rel, Change: changeKind(hadOld, hasNew)}

		switch {
		case rel == derivedIndexPath:
			file.Derived = true
		case isGeneratedFactsPath(rel) && hasNew:
			// A facts file the publisher generated: judge it fact by fact.
			file.PerEntry = true
			live := factIDs(oldText)
			for _, block := range parseFactsFile(newText).blocks() {
				if _, alreadyLive := live[block.id]; alreadyLive {
					continue
				}
				file.Entries = append(file.Entries, ReviewEntry{
					FactID: block.id, Scope: block.scope, Body: block.body,
				})
			}
			out.NewFacts += len(file.Entries)
		}

		viewText := newText
		if !hasNew {
			viewText = oldText
		}
		if int64(len(viewText)) > reviewDiffLimit {
			file.Oversized = true
			file.Bytes = int64(len(viewText))
		} else {
			file.Content = viewText
		}

		if !file.PerEntry {
			if size := int64(len(newText) + len(oldText)); size > reviewDiffLimit {
				file.Oversized = true
				if file.Bytes == 0 {
					file.Bytes = int64(len(newText))
				}
			} else {
				file.Diff = k.diffLines(current, pending.Digest, rel, hadOld, hasNew)
			}
		}
		out.Files = append(out.Files, file)
	}
	return out, nil
}

// ReviewDecisions is a reviewer's verdict on a staged snapshot. Anything not
// named is treated as NOT approved: a partial submission re-stages only what was
// explicitly accepted, so a forgotten entry cannot reach a sandbox by default.
type ReviewDecisions struct {
	// Digest guards against a stale page: it must be the snapshot currently
	// pending, so decisions always apply to the bytes the reviewer looked at.
	Digest string `json:"digest"`
	// Facts maps a fact id to "approved" or "rejected".
	Facts map[string]string `json:"facts"`
	// Files maps a whole-file path to "approved" or "rejected".
	Files map[string]string `json:"files"`
	Actor string            `json:"actor"`
	// Publish promotes the re-aggregated snapshot immediately instead of leaving
	// it staged for a second look.
	Publish bool `json:"publish"`
}

// ReviewResult reports what the aggregation produced.
type ReviewResult struct {
	// Digest is the re-aggregated snapshot. Equal to the input digest when
	// everything was approved.
	Digest string `json:"digest"`
	// Staged is false when the approved subset is byte-identical to what is
	// already live — there is nothing left to publish, and the pending marker is
	// cleared.
	Staged    bool `json:"staged"`
	Published bool `json:"published"`
	// RejectedFactIDs are the numeric ids the caller must demote in the memory DB
	// so the next cycle does not re-render them.
	RejectedFactIDs []int64 `json:"rejected_fact_ids"`
	ApprovedFacts   int     `json:"approved_facts"`
	RejectedFacts   int     `json:"rejected_facts"`
	ApprovedFiles   int     `json:"approved_files"`
	RejectedFiles   int     `json:"rejected_files"`
}

// ApplyReview rebuilds the snapshot from the approved entries and stages the
// result. `current` is untouched unless d.Publish is set.
func (k *KB) ApplyReview(d ReviewDecisions) (ReviewResult, error) {
	var res ReviewResult
	if k == nil {
		return res, fmt.Errorf("kb: disabled (no root configured)")
	}
	pending, err := k.PendingInfo()
	if err != nil {
		return res, err
	}
	if pending == nil {
		return res, fmt.Errorf("kb: nothing pending approval")
	}
	if d.Digest != "" && d.Digest != pending.Digest {
		return res, fmt.Errorf("kb: digest %q is not the pending snapshot (pending is %q)", d.Digest, pending.Digest)
	}
	before, err := readTree(k.SnapshotDir(k.CurrentDigest()))
	if err != nil {
		return res, fmt.Errorf("kb: read current snapshot: %w", err)
	}
	after, err := readTree(k.SnapshotDir(pending.Digest))
	if err != nil {
		return res, fmt.Errorf("kb: read pending snapshot: %w", err)
	}

	// Start from what is live and fold in only what was approved, so an entry
	// reaches the result by explicit acceptance rather than by default.
	out := map[string]string{}
	for rel, text := range before {
		out[rel] = text
	}

	approved := func(m map[string]string, key string) bool {
		return strings.EqualFold(strings.TrimSpace(m[key]), "approved")
	}

	for _, rel := range changedPaths(before, after) {
		oldText, hadOld := before[rel]
		newText, hasNew := after[rel]

		// The index is a projection of everything else; regenerated below.
		if rel == derivedIndexPath {
			continue
		}

		if isGeneratedFactsPath(rel) && hasNew {
			live := factIDs(oldText)
			parsed := parseFactsFile(newText)
			kept := parsed.filter(func(b factBlock) bool {
				if _, alreadyLive := live[b.id]; alreadyLive {
					return true // already published; not under review
				}
				if approved(d.Facts, b.id) {
					res.ApprovedFacts++
					return true
				}
				res.RejectedFacts++
				if id, convErr := strconv.ParseInt(b.id, 10, 64); convErr == nil {
					res.RejectedFactIDs = append(res.RejectedFactIDs, id)
				}
				return false
			})
			if body, any := kept.render(); any {
				out[rel] = body
			} else {
				// Every fact in the file was rejected, so the file itself has no
				// reason to exist. Dropping it (rather than leaving an empty
				// heading) is also what the renderer would have produced.
				delete(out, rel)
			}
			continue
		}

		// Whole-file decision.
		if approved(d.Files, rel) {
			res.ApprovedFiles++
			if hasNew {
				out[rel] = newText
			} else {
				delete(out, rel) // approving a removal removes it
			}
			continue
		}
		res.RejectedFiles++
		if hadOld {
			out[rel] = oldText
		} else {
			delete(out, rel)
		}
	}

	if index, ok := after[derivedIndexPath]; ok {
		out[derivedIndexPath] = refreshIndex(index, out)
	}

	sort.Slice(res.RejectedFactIDs, func(i, j int) bool { return res.RejectedFactIDs[i] < res.RejectedFactIDs[j] })

	digest, err := k.writeSnapshot(out)
	if err != nil {
		return res, err
	}
	res.Digest = digest

	if digest == k.CurrentDigest() {
		// Nothing survived review that is not already live. Clear the marker so
		// `kb status` does not advertise a pending snapshot equal to current.
		if err := k.clearPending(); err != nil {
			return res, err
		}
		return res, nil
	}

	next := Pending{
		Digest:     digest,
		PrevDigest: k.CurrentDigest(),
		StagedAt:   pending.StagedAt,
		RunID:      pending.RunID,
		Counts:     pending.Counts,
	}
	if err := k.writePending(next); err != nil {
		return res, err
	}
	res.Staged = true

	if d.Publish {
		if err := k.Approve(digest, d.Actor); err != nil {
			return res, err
		}
		res.Published = true
		res.Staged = false
	}
	return res, nil
}

// --- facts file parsing -----------------------------------------------------
//
// The shapes below mirror renderFactsFile exactly: marker line, generated-by
// header, `# Facts: <title>`, then `## <scope>` sections each holding
// `<!-- fact-id:N -->` + one rendered bullet. Re-emitting through the same
// structure (rather than splicing strings) is what keeps a partially approved
// file byte-identical to what the renderer would have produced for that subset —
// which matters because the digest is over the bytes.

var factIDMarker = regexp.MustCompile(`^<!--\s*fact-id:(\d+)\s*-->$`)

type factBlock struct {
	id    string
	scope string
	body  string
}

type factsScope struct {
	name   string
	blocks []factBlock
}

type factsFile struct {
	header string // everything before the first `## ` scope heading
	scopes []factsScope
}

func parseFactsFile(text string) factsFile {
	var out factsFile
	var header []string
	lines := strings.Split(text, "\n")
	i := 0
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			break
		}
		header = append(header, lines[i])
	}
	out.header = strings.TrimRight(strings.Join(header, "\n"), "\n")

	var scope *factsScope
	var pending *factBlock
	flush := func() {
		if scope != nil && pending != nil {
			pending.body = strings.TrimRight(pending.body, "\n")
			scope.blocks = append(scope.blocks, *pending)
		}
		pending = nil
	}
	closeScope := func() {
		flush()
		if scope != nil {
			out.scopes = append(out.scopes, *scope)
		}
		scope = nil
	}
	for ; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "## ") {
			closeScope()
			name := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			scope = &factsScope{name: name}
			continue
		}
		if m := factIDMarker.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			flush()
			pending = &factBlock{id: m[1], scope: scopeName(scope)}
			continue
		}
		if pending != nil {
			if strings.TrimSpace(line) == "" {
				flush()
				continue
			}
			pending.body += line + "\n"
		}
	}
	closeScope()
	return out
}

func scopeName(s *factsScope) string {
	if s == nil {
		return "global"
	}
	return s.name
}

func (f factsFile) blocks() []factBlock {
	var out []factBlock
	for _, scope := range f.scopes {
		out = append(out, scope.blocks...)
	}
	return out
}

// filter keeps the blocks keep() accepts, dropping scopes left empty.
func (f factsFile) filter(keep func(factBlock) bool) factsFile {
	out := factsFile{header: f.header}
	for _, scope := range f.scopes {
		next := factsScope{name: scope.name}
		for _, block := range scope.blocks {
			if keep(block) {
				next.blocks = append(next.blocks, block)
			}
		}
		if len(next.blocks) > 0 {
			out.scopes = append(out.scopes, next)
		}
	}
	return out
}

// render re-emits the file. any is false when no facts survived, which tells the
// caller to drop the file entirely.
func (f factsFile) render() (text string, any bool) {
	if len(f.scopes) == 0 {
		return "", false
	}
	var b strings.Builder
	b.WriteString(f.header)
	b.WriteString("\n\n")
	for _, scope := range f.scopes {
		fmt.Fprintf(&b, "## %s\n\n", scope.name)
		for _, block := range scope.blocks {
			fmt.Fprintf(&b, "<!-- fact-id:%s -->\n", block.id)
			b.WriteString(block.body)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String(), true
}

func factIDs(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, block := range parseFactsFile(text).blocks() {
		out[block.id] = struct{}{}
	}
	return out
}

// isGeneratedFactsPath reports whether rel is a facts file this publisher owns.
// A HUMAN-SEEDED facts file carries no generated marker and no fact ids, so it is
// reviewed as a whole file — splitting it per entry would offer decisions on
// content the pipeline cannot re-render.
func isGeneratedFactsPath(rel string) bool {
	return strings.HasPrefix(rel, "facts/") && rel != derivedIndexPath && strings.HasSuffix(rel, ".md")
}

// --- index regeneration -----------------------------------------------------

// indexBullet matches a listing line in facts/INDEX.md and captures the KB path
// it points at. renderIndex emits one such bullet per skill and per fact group.
var indexBullet = regexp.MustCompile("^- .*`((?:facts|skills)/[^`]+)`")

// factCount matches the "— N fact(s)" tail of a fact-group bullet.
var factCount = regexp.MustCompile(`— \d+ fact\(s\)`)

// emptySectionMarker is what renderIndex writes for a section with no items.
const emptySectionMarker = "_(none yet)_"

// indexListingSections are the headings whose bullets name real files. `## Layout`
// is deliberately excluded: its bullets are illustrative (`skills/<name>/SKILL.md`
// is a pattern, not a path), so filtering it against the tree would delete the
// documentation on every review.
var indexListingSections = map[string]bool{"## Skills": true, "## Fact groups": true}

// refreshIndex rewrites facts/INDEX.md to describe the tree that actually
// survived review.
//
// It filters the STAGED index rather than re-rendering from scratch: re-rendering
// needs the memory rows and skill candidates that produced it, which a partial
// approval does not have, and would drift from renderIndex the moment that
// function changes. Filtering enforces one rule inside the listing sections — a
// bullet may only name a path that exists in the result tree — which covers both
// a rejected skill file and a facts file whose count shrank.
func refreshIndex(index string, tree map[string]string) string {
	lines := strings.Split(index, "\n")
	out := make([]string, 0, len(lines))
	inListing := false
	kept := 0
	// Where the marker goes if the section ends up empty: after the heading,
	// before the blank line that separates sections.
	markerAt := -1

	closeSection := func() {
		if inListing && kept == 0 && markerAt >= 0 {
			out = append(out[:markerAt], append([]string{emptySectionMarker}, out[markerAt:]...)...)
		}
		inListing, kept, markerAt = false, 0, -1
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "## ") {
			closeSection()
			out = append(out, line)
			if indexListingSections[strings.TrimSpace(line)] {
				inListing, kept, markerAt = true, 0, len(out)
			}
			continue
		}
		if !inListing {
			out = append(out, line)
			continue
		}
		if strings.TrimSpace(line) == emptySectionMarker {
			continue // re-decided below from what actually survived
		}
		match := indexBullet.FindStringSubmatch(line)
		if match == nil {
			out = append(out, line)
			continue
		}
		body, exists := tree[match[1]]
		if !exists {
			continue // the file did not survive review
		}
		if isGeneratedFactsPath(match[1]) {
			// Counts shift when individual facts inside the file are rejected.
			line = factCount.ReplaceAllString(line, fmt.Sprintf("— %d fact(s)", len(factIDs(body))))
		}
		out = append(out, line)
		kept++
		markerAt = len(out)
	}
	closeSection()
	return strings.Join(out, "\n")
}

// --- tree helpers -----------------------------------------------------------

func changedPaths(before, after map[string]string) []string {
	seen := map[string]struct{}{}
	for rel := range before {
		seen[rel] = struct{}{}
	}
	for rel := range after {
		seen[rel] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for rel := range seen {
		if before[rel] != after[rel] {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func changeKind(hadOld, hasNew bool) string {
	switch {
	case !hadOld:
		return "added"
	case !hasNew:
		return "removed"
	default:
		return "modified"
	}
}

// diffLines shells out to `diff -u`, matching kbDiff: a reviewer reads the same
// format here as in the CLI, and we do not maintain a bespoke differ.
func (k *KB) diffLines(current, pending, rel string, hadOld, hasNew bool) []ReviewLine {
	oldPath, newPath := os.DevNull, os.DevNull
	if hadOld {
		oldPath = filepath.Join(k.SnapshotDir(current), rel)
	}
	if hasNew {
		newPath = filepath.Join(k.SnapshotDir(pending), rel)
	}
	cmd := exec.Command("diff", "-u", "-U", "4", "--label", "current", "--label", "pending", oldPath, newPath)
	stdout, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() > 1 {
			return nil
		}
	}
	var out []ReviewLine
	for i, line := range strings.Split(string(stdout), "\n") {
		if i < 2 || line == `\ No newline at end of file` {
			continue // the ---/+++ header carries no information the UI lacks
		}
		switch {
		case strings.HasPrefix(line, "@@"):
			out = append(out, ReviewLine{Kind: "hunk", Text: line})
		case strings.HasPrefix(line, "+"):
			out = append(out, ReviewLine{Kind: "add", Text: line[1:]})
		case strings.HasPrefix(line, "-"):
			out = append(out, ReviewLine{Kind: "del", Text: line[1:]})
		default:
			out = append(out, ReviewLine{Kind: "ctx", Text: strings.TrimPrefix(line, " ")})
		}
	}
	return out
}

// readTree reads a snapshot dir into rel-path → content. A missing dir (no
// snapshot published yet) is an empty tree, not an error.
func readTree(dir string) (map[string]string, error) {
	out := map[string]string{}
	if dir == "" {
		return out, nil
	}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out[filepath.ToSlash(rel)] = string(raw)
		return nil
	})
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	return out, err
}
