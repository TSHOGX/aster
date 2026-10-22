package examples

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"aster/contracts/evolution"
	manifest "aster/contracts/volumemanifest"
	"aster/evolution-space/internal/workflow"
)

var proposalID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func knownFamily(e Episode) bool {
	return e.FamilyKind == "local" || e.FamilyKind == "copy" || e.FamilyKind == "declared"
}
func indexCorpus(corpus Corpus) (map[string]Episode, error) {
	episodes := map[string]Episode{}
	for _, episode := range corpus.Episodes {
		if !manifest.ValidID(episode.ID) || !manifest.ValidID(episode.FamilyID) || episodes[episode.ID].ID != "" {
			return nil, workflow.Failure{Code: "invalid_corpus_identity"}
		}
		episodes[episode.ID] = episode
	}
	return episodes, nil
}

// evidenceHold applies the class rule to the cited episodes. Support counts source
// families, so copies of one conversation never look like independent evidence.
func evidenceHold(c Class, episodes map[string]Episode, ids []string) string {
	if len(ids) == 0 {
		return "missing_evidence"
	}
	completed, failed := 0, 0
	families := map[string]bool{}
	for _, id := range ids {
		episode, ok := episodes[id]
		switch {
		case !ok:
			return "unknown_evidence"
		case episode.Truncated:
			return "truncated_evidence"
		case !knownFamily(episode):
			return "unknown_provenance"
		case episode.Outcome == "completed":
			completed++
		case episode.Outcome == "failed":
			failed++
		default:
			return "unknown_outcome"
		}
		families[episode.FamilyID] = true
	}
	switch c.Evidence {
	case EvidenceCompleted:
		if failed > 0 {
			return "unsuccessful_evidence"
		}
	case EvidenceResolved:
		if completed == 0 {
			return "unresolved"
		}
	case EvidenceSingleCase:
		if len(families) != 1 {
			return "multiple_cases"
		}
	}
	return ""
}

func fieldHold(c Class, item Proposal) (string, error) {
	for name := range item.Attributes {
		if _, ok := c.field(name); !ok {
			return "", workflow.Failure{Code: "invalid_proposal"}
		}
	}
	for _, f := range c.Fields {
		value, present := item.Attributes[f.Name]
		if strings.ContainsRune(value, 0) || len(value) > f.MaxBytes {
			return "", workflow.Failure{Code: "invalid_proposal"}
		}
		value = strings.TrimSpace(value)
		if value == "" {
			if f.Required {
				return "missing_field", nil
			}
			if present {
				delete(item.Attributes, f.Name)
			}
			continue
		}
		item.Attributes[f.Name] = value
		if !f.Multiline && !singleLine(value) {
			return "invalid_field", nil
		}
		if len(f.Enum) > 0 {
			allowed := false
			for _, option := range f.Enum {
				allowed = allowed || value == option
			}
			if !allowed {
				return "invalid_field", nil
			}
		}
	}
	if c.check != nil {
		return c.check(item), nil
	}
	return "", nil
}

var holdReason = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// verify is the deterministic gate shared by every starter graph. Malformed
// structure fails the node; weak, unsafe or contradictory content is held with a
// reason so reviewers can see what was not promoted and why.
func verify(c Class, corpus Corpus, items []Proposal) ([]Proposal, error) {
	episodes, err := indexCorpus(corpus)
	if err != nil {
		return nil, err
	}
	if len(items) > maxProposalsPerRun {
		return nil, workflow.Failure{Code: "proposal_limit"}
	}
	result := []Proposal{}
	seen := map[string]bool{}
	for _, item := range items {
		if !proposalID.MatchString(item.ID) || seen[item.ID] || strings.TrimSpace(item.Title) == "" || len(item.Title) > 200 || !singleLine(item.Title) || strings.ContainsRune(item.Title, 0) || strings.TrimSpace(item.Body) == "" || len(item.Body) > 16384 || strings.ContainsRune(item.Body, 0) || (item.Status != "supported" && item.Status != "held") || len(item.EvidenceIDs) > 100 {
			return nil, workflow.Failure{Code: "invalid_proposal"}
		}
		seen[item.ID] = true
		attributes := map[string]string{}
		for name, value := range item.Attributes {
			attributes[name] = value
		}
		item.Attributes = attributes
		reason, err := fieldHold(c, item)
		if err != nil {
			return nil, err
		}
		ids := map[string]bool{}
		unique := []string{}
		for _, id := range item.EvidenceIDs {
			if !ids[id] {
				ids[id] = true
				unique = append(unique, id)
			}
		}
		if reason == "" {
			reason = evidenceHold(c, episodes, unique)
		}
		if reason == "" && containsSensitive(item) {
			reason = "sensitive_content"
		}
		if reason == "" && item.Status == "held" {
			reason = item.HoldReason
			if !holdReason.MatchString(reason) {
				reason = "held_by_agent"
			}
		}
		if reason != "" {
			item.Status, item.HoldReason = "held", reason
			item.EvidenceIDs = unique
		} else {
			item.Status, item.HoldReason = "supported", ""
			families := map[string]string{}
			for _, id := range unique {
				family := episodes[id].FamilyID
				if prior := families[family]; prior == "" || id < prior {
					families[family] = id
				}
			}
			item.EvidenceIDs = make([]string, 0, len(families))
			for _, id := range families {
				item.EvidenceIDs = append(item.EvidenceIDs, id)
			}
		}
		sort.Strings(item.EvidenceIDs)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// yamlString writes a JSON string literal, which is a valid YAML double-quoted scalar.
func yamlString(value string) string { raw, _ := json.Marshal(value); return string(raw) }

func render(c Class, item Proposal) string {
	var out strings.Builder
	out.WriteString("---\n")
	evidence := strings.Join(item.EvidenceIDs, ", ")
	if c.Template == "example-skills" {
		// Agent Skills front matter: name and description are what agents index.
		fmt.Fprintf(&out, "name: %s\ndescription: %s\nmetadata:\n  memory-class: %s\n  source-template: %s\n  evidence-ids: %s\n", item.ID, yamlString(item.Attributes["when_to_use"]), c.MemoryClass, c.Template, yamlString(evidence))
	} else {
		fmt.Fprintf(&out, "id: %s\nmemory_class: %s\ntype: %s\n", item.ID, c.MemoryClass, c.Kind)
		for _, f := range c.Fields {
			if value := item.Attributes[f.Name]; value != "" && !f.Multiline {
				fmt.Fprintf(&out, "%s: %s\n", f.Name, yamlString(value))
			}
		}
		fmt.Fprintf(&out, "source_template: %s\nevidence_ids: [%s]\n", c.Template, evidence)
	}
	fmt.Fprintf(&out, "---\n\n# %s\n\n%s\n", strings.TrimSpace(item.Title), strings.TrimSpace(item.Body))
	for _, f := range c.Fields {
		if value := item.Attributes[f.Name]; value != "" && f.Multiline {
			fmt.Fprintf(&out, "\n## %s\n\n%s\n", f.Heading, value)
		}
	}
	fmt.Fprintf(&out, "\nEvidence IDs: %s\n", evidence)
	return out.String()
}

func candidate(c Class, items []Proposal, skipped []Proposal) (Candidate, error) {
	result := Candidate{Files: []File{}, Held: append([]Proposal{}, skipped...)}
	for _, item := range items {
		if item.Status != "supported" {
			result.Held = append(result.Held, item)
			continue
		}
		path := c.file(item)
		if manifest.ValidAuthoringPath(path, false) != nil {
			return result, workflow.Failure{Code: "invalid_candidate_path"}
		}
		result.Files = append(result.Files, File{Path: path, Text: render(c, item)})
	}
	if len(result.Files) > 100 || len(result.Held) > 1000 {
		return result, workflow.Failure{Code: "candidate_limit"}
	}
	return result, nil
}

func decodeInput[T any](values workflow.Values, name string) (T, error) {
	return evolution.Decode[T](bytes.NewReader(values[name]))
}
func output(name string, value any) (workflow.Values, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, workflow.Failure{Code: "invalid_node_output"}
	}
	return workflow.Values{name: raw}, nil
}

// eligibleCases picks one representative per source family in corpus order and
// reports every other episode with the reason it gets no case record.
func eligibleCases(corpus Corpus) ([]Episode, []Proposal) {
	cases, skipped := []Episode{}, []Proposal{}
	representative := map[string]bool{}
	skip := func(e Episode, reason string) {
		skipped = append(skipped, Proposal{ID: "skipped", Title: "No case record", Body: "This episode was not recorded: " + reason + ".", EvidenceIDs: []string{e.ID}, Status: "held", HoldReason: reason, Attributes: map[string]string{}})
	}
	for _, e := range corpus.Episodes {
		switch {
		case e.Truncated:
			skip(e, "truncated_evidence")
		case !knownFamily(e):
			skip(e, "unknown_provenance")
		case e.Outcome != "completed" && e.Outcome != "failed":
			skip(e, "unknown_outcome")
		case representative[e.FamilyID]:
			// Copies describe the same event as the family representative.
		case len(cases) == maxEpisodeRecords:
			representative[e.FamilyID] = true
			skip(e, "batch_limit")
		default:
			representative[e.FamilyID] = true
			cases = append(cases, e)
		}
	}
	return cases, skipped
}

type mapRecord struct {
	State     string     `json:"state"`
	Value     []Proposal `json:"value,omitempty"`
	ErrorCode string     `json:"error_code,omitempty"`
}

// gather assigns unique case IDs in episode order and captures map failures as
// held items, so one unreadable episode never discards the other records.
func gather(corpus Corpus, records []mapRecord) ([]Proposal, []Proposal, error) {
	cases, skipped := eligibleCases(corpus)
	if len(records) != len(cases) {
		return nil, nil, workflow.Failure{Code: "record_count_mismatch"}
	}
	items := []Proposal{}
	used := map[string]bool{}
	for i, record := range records {
		episode := cases[i]
		if record.State != "succeeded" || len(record.Value) != 1 {
			code := record.ErrorCode
			if !holdReason.MatchString(code) {
				code = "record_failed"
			}
			skipped = append(skipped, Proposal{ID: "skipped", Title: "Case record not written", Body: "Recording this episode did not produce one case record (" + code + ").", EvidenceIDs: []string{episode.ID}, Status: "held", HoldReason: "record_failed", Attributes: map[string]string{}})
			continue
		}
		item := record.Value[0]
		base := item.ID
		if !proposalID.MatchString(base) {
			return nil, nil, workflow.Failure{Code: "invalid_proposal"}
		}
		id := base
		for n := 2; used[id]; n++ {
			suffix := fmt.Sprintf("-%d", n)
			id = strings.TrimRight(base[:min(len(base), 64-len(suffix))], "-") + suffix
		}
		used[id] = true
		item.ID = id
		items = append(items, item)
	}
	return items, skipped, nil
}

// caseOutcome rejects a record whose stated outcome disagrees with its evidence.
func caseOutcome(c Class, corpus Corpus, items []Proposal) ([]Proposal, error) {
	verified, err := verify(c, corpus, items)
	if err != nil {
		return nil, err
	}
	episodes, _ := indexCorpus(corpus)
	for i, item := range verified {
		if item.Status != "supported" {
			continue
		}
		outcome := "failed"
		for _, id := range item.EvidenceIDs {
			if episodes[id].Outcome == "completed" {
				outcome = "completed"
			}
		}
		if item.Attributes["outcome"] != outcome {
			verified[i].Status, verified[i].HoldReason = "held", "outcome_mismatch"
		}
	}
	return verified, nil
}

// Transform matches runner.Transform without importing the runner package.
type Transform = func(context.Context, workflow.Call) (workflow.Values, error)

// Implementations registers every deterministic starter node by exact revision.
func Implementations() map[evolution.Revision]Transform {
	classes := map[string]Class{}
	for _, c := range Classes() {
		classes[c.Template] = c
	}
	check := func(c Class) func(Corpus, []Proposal) ([]Proposal, error) {
		if c.Evidence == EvidenceSingleCase {
			return func(corpus Corpus, items []Proposal) ([]Proposal, error) { return caseOutcome(c, corpus, items) }
		}
		return func(corpus Corpus, items []Proposal) ([]Proposal, error) { return verify(c, corpus, items) }
	}
	result := map[evolution.Revision]Transform{}
	for template, ref := range verifyFor {
		c := classes[template]
		result[ref] = func(_ context.Context, call workflow.Call) (workflow.Values, error) {
			corpus, err := decodeInput[Corpus](call.Inputs, "corpus")
			if err != nil {
				return nil, workflow.Failure{Code: "invalid_corpus"}
			}
			items, err := decodeInput[[]Proposal](call.Inputs, "items")
			if err != nil {
				return nil, workflow.Failure{Code: "invalid_proposals"}
			}
			if items, err = check(c)(corpus, items); err != nil {
				return nil, err
			}
			return output("items", items)
		}
	}
	for template, ref := range buildFor {
		c := classes[template]
		result[ref] = func(_ context.Context, call workflow.Call) (workflow.Values, error) {
			corpus, err := decodeInput[Corpus](call.Inputs, "corpus")
			if err != nil {
				return nil, workflow.Failure{Code: "invalid_corpus"}
			}
			var items, skipped []Proposal
			if c.Template == "example-skills" {
				state, err := decodeInput[State](call.Inputs, "state")
				if err != nil || !state.Valid {
					return nil, workflow.Failure{Code: "unreviewed_skill_state"}
				}
				original, _ := json.Marshal(corpus)
				retained, _ := json.Marshal(state.Corpus)
				if !bytes.Equal(original, retained) {
					return nil, workflow.Failure{Code: "critic_changed_corpus"}
				}
				items = state.Items
			} else if items, err = decodeInput[[]Proposal](call.Inputs, "items"); err != nil {
				return nil, workflow.Failure{Code: "invalid_proposals"}
			}
			if _, ok := call.Inputs["skipped"]; ok {
				if skipped, err = decodeInput[[]Proposal](call.Inputs, "skipped"); err != nil {
					return nil, workflow.Failure{Code: "invalid_proposals"}
				}
			}
			// Builders re-verify so a graph edit that bypasses the validator cannot
			// promote unsupported evidence into files.
			if items, err = check(c)(corpus, items); err != nil {
				return nil, err
			}
			result, err := candidate(c, items, skipped)
			if err != nil {
				return nil, err
			}
			return output("candidate", result)
		}
	}
	result[SplitEpisodes] = func(_ context.Context, call workflow.Call) (workflow.Values, error) {
		corpus, err := decodeInput[Corpus](call.Inputs, "corpus")
		if err != nil {
			return nil, workflow.Failure{Code: "invalid_corpus"}
		}
		if _, err = indexCorpus(corpus); err != nil {
			return nil, err
		}
		cases, _ := eligibleCases(corpus)
		batch := make([]Corpus, 0, len(cases))
		for _, episode := range cases {
			batch = append(batch, Corpus{Episodes: []Episode{episode}})
		}
		return output("episodes", batch)
	}
	result[GatherEpisodes] = func(_ context.Context, call workflow.Call) (workflow.Values, error) {
		corpus, err := decodeInput[Corpus](call.Inputs, "corpus")
		if err != nil {
			return nil, workflow.Failure{Code: "invalid_corpus"}
		}
		records, err := decodeInput[[]mapRecord](call.Inputs, "records")
		if err != nil {
			return nil, workflow.Failure{Code: "invalid_records"}
		}
		items, skipped, err := gather(corpus, records)
		if err != nil {
			return nil, err
		}
		values, _ := output("items", items)
		held, _ := output("skipped", skipped)
		values["skipped"] = held["skipped"]
		return values, nil
	}
	return result
}
