package examples

import (
	"encoding/json"
	"path"
	"regexp"
	"strings"
)

// Starter templates follow the common agent-memory taxonomy (CoALA and later
// surveys): episodic records of what happened, semantic knowledge of what is
// true, and procedural knowledge of how to act. Each class has its own fields,
// evidence rule and file layout; nothing forces one schema on every goal.
const (
	Episodic   = "episodic"
	Semantic   = "semantic"
	Procedural = "procedural"
)

// Field is one class-specific proposal attribute. Single-line fields appear in
// file front matter; multiline fields become Markdown sections.
type Field struct {
	Name      string   `json:"name"`
	Heading   string   `json:"heading"`
	Required  bool     `json:"required"`
	Multiline bool     `json:"multiline"`
	Enum      []string `json:"enum,omitempty"`
	MaxBytes  int      `json:"max_bytes"`
	Guidance  string   `json:"guidance"`
}

// Evidence rules are deterministic and independent of model agreement.
const (
	// Every cited episode completed: a procedure must have been demonstrated.
	EvidenceCompleted = "all_completed"
	// At least one completed episode; failed episodes may add context or a trigger.
	EvidenceResolved = "resolved"
	// One source family with a known outcome: a case record describes one event.
	EvidenceSingleCase = "single_case"
)

type Class struct {
	Template    string  `json:"template"`
	MemoryClass string  `json:"memory_class"`
	Kind        string  `json:"kind"`
	Goal        string  `json:"goal"`
	Prefix      string  `json:"prefix"`
	Layout      string  `json:"layout"`
	Evidence    string  `json:"evidence"`
	Fields      []Field `json:"fields"`
	// check applies cross-field rules after per-field validation.
	check func(Proposal) string
	// file returns the output-relative path for a supported proposal.
	file func(Proposal) string
}

func (c Class) field(name string) (Field, bool) {
	for _, f := range c.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

var supersedesField = Field{Name: "supersedes", Heading: "Supersedes", MaxBytes: 64, Guidance: "ID of an earlier item this one replaces, only when the evidence shows the claim changed."}

// checkSupersedes covers the update half of memory evolution. A revision reuses
// the item's stable ID and replaces its file; supersedes names a different item
// for the reviewer to retire. Deletion is an expert decision in Studio, never
// inferred from an absent proposal.
func checkSupersedes(item Proposal) string {
	if target := item.Attributes["supersedes"]; target != "" && (!proposalID.MatchString(target) || target == item.ID) {
		return "invalid_supersedes"
	}
	return ""
}

var knowledgeDirectories = map[string]string{"entity": "entities", "dimension": "dimensions", "metric": "metrics", "relationship": "relationships", "term": "glossary"}

// Classes returns the built-in starter definitions in catalog order. The first
// two keep their historical template IDs and output prefixes.
func Classes() []Class {
	flat := func(item Proposal) string { return item.ID + ".md" }
	return []Class{
		{
			Template: "example-skills", MemoryClass: Procedural, Kind: "skill", Prefix: "skills", Layout: "<id>/SKILL.md", Evidence: EvidenceCompleted,
			Goal: "Procedural memory: reusable skills drafted from successful episodes, with bounded critique and revision",
			Fields: []Field{
				{Name: "when_to_use", Heading: "When to use", Required: true, MaxBytes: 1024, Guidance: "One line naming the task and the observable situation that calls for this skill."},
				{Name: "prerequisites", Heading: "Prerequisites", Multiline: true, MaxBytes: 4096, Guidance: "Access, tools, inputs and state that must exist before step one."},
				{Name: "verification", Heading: "Verification", Required: true, Multiline: true, MaxBytes: 4096, Guidance: "How to check that the procedure succeeded, with observable results."},
				{Name: "pitfalls", Heading: "Known pitfalls", Multiline: true, MaxBytes: 4096, Guidance: "Failure modes seen in the evidence and how to recover from them."},
			},
			file: func(item Proposal) string { return item.ID + "/SKILL.md" },
		},
		{
			Template: "example-corrections", MemoryClass: Procedural, Kind: "correction", Prefix: "lessons", Layout: "<id>.md", Evidence: EvidenceResolved,
			Goal: "Procedural memory: corrective lessons captured from failures and user corrections, each backed by a resolved episode",
			Fields: []Field{
				{Name: "scope", Heading: "Scope", Required: true, MaxBytes: 128, Guidance: "Short domain label such as billing, sql or deployment."},
				{Name: "trigger", Heading: "Trigger", Required: true, Multiline: true, MaxBytes: 4096, Guidance: "The observable situation where the mistake happens."},
				{Name: "mistaken_assumption", Heading: "Mistaken assumption", Required: true, Multiline: true, MaxBytes: 4096, Guidance: "What was believed or done that turned out to be wrong."},
				{Name: "corrective_action", Heading: "Corrective action", Required: true, Multiline: true, MaxBytes: 4096, Guidance: "What to do instead, as concrete steps."},
				{Name: "expected_result", Heading: "Expected result", Required: true, Multiline: true, MaxBytes: 4096, Guidance: "What should be observed when the corrective action works."},
				{Name: "counterexample", Heading: "Counterexample", Multiline: true, MaxBytes: 4096, Guidance: "When this lesson does not apply."},
			},
			file: flat,
		},
		{
			Template: "example-facts", MemoryClass: Semantic, Kind: "fact", Prefix: "facts", Layout: "<id>.md", Evidence: EvidenceResolved,
			Goal: "Semantic memory: atomic, durable facts, preferences, decisions and constraints with scope, valid time and supersession",
			Fields: []Field{
				{Name: "kind", Heading: "Kind", Required: true, Enum: []string{"fact", "preference", "decision", "constraint"}, MaxBytes: 16, Guidance: "One of the listed kinds."},
				{Name: "scope", Heading: "Scope", Required: true, MaxBytes: 128, Guidance: "Short domain label such as billing, sql or deployment."},
				{Name: "valid_from", Heading: "Valid from", MaxBytes: 64, Guidance: "ISO 8601 date when the claim became true, if the evidence states it."},
				supersedesField,
			},
			check: checkSupersedes,
			file:  flat,
		},
		{
			Template: "example-knowledge", MemoryClass: Semantic, Kind: "semantic-layer", Prefix: "knowledge", Layout: "<entities|dimensions|metrics|relationships|glossary>/<id>.md", Evidence: EvidenceResolved,
			Goal: "Semantic memory: a business semantic layer of entities, dimensions, metrics, relationships and terms, consolidated against authoritative sources",
			Fields: []Field{
				{Name: "kind", Heading: "Kind", Required: true, Enum: []string{"entity", "dimension", "metric", "relationship", "term"}, MaxBytes: 16, Guidance: "One of the listed kinds."},
				{Name: "source", Heading: "Authoritative source", Required: true, MaxBytes: 256, Guidance: "Authoritative system, table, document or owner named in the evidence."},
				{Name: "entity", Heading: "Entity", MaxBytes: 128, Guidance: "Entity ID this dimension or metric belongs to. Required for dimensions and metrics."},
				{Name: "unit", Heading: "Unit", MaxBytes: 64, Guidance: "Measurement unit. Required for metrics."},
				{Name: "grain", Heading: "Grain", MaxBytes: 256, Guidance: "Row or time grain the metric is computed at. Required for metrics."},
				{Name: "synonyms", Heading: "Synonyms", MaxBytes: 512, Guidance: "Comma-separated alternative names users employ."},
				{Name: "formula", Heading: "Formula", Multiline: true, MaxBytes: 4096, Guidance: "Exact calculation, filters and time window. Required for metrics."},
				supersedesField,
			},
			check: func(item Proposal) string {
				a := item.Attributes
				switch a["kind"] {
				case "metric":
					if a["formula"] == "" || a["unit"] == "" || a["grain"] == "" || a["entity"] == "" {
						return "incomplete_metric"
					}
				case "dimension":
					if a["entity"] == "" {
						return "missing_entity"
					}
				}
				return checkSupersedes(item)
			},
			file: func(item Proposal) string {
				return path.Join(knowledgeDirectories[item.Attributes["kind"]], item.ID+".md")
			},
		},
		{
			Template: "example-episodes", MemoryClass: Episodic, Kind: "case-record", Prefix: "episodes", Layout: "<id>.md", Evidence: EvidenceSingleCase,
			Goal: "Episodic memory: one reviewed case record per source episode, covering situation, actions, outcome and takeaway",
			Fields: []Field{
				{Name: "outcome", Heading: "Outcome", Required: true, Enum: []string{"completed", "failed"}, MaxBytes: 16, Guidance: "completed when a cited episode completed; failed when every cited episode failed."},
				{Name: "situation", Heading: "Situation", Required: true, Multiline: true, MaxBytes: 4096, Guidance: "Task, goal and relevant context at the start."},
				{Name: "actions", Heading: "Actions", Required: true, Multiline: true, MaxBytes: 4096, Guidance: "Key steps actually taken, including errors and retries."},
				{Name: "takeaway", Heading: "Takeaway", Multiline: true, MaxBytes: 4096, Guidance: "What to repeat or avoid next time, if anything generalizes."},
			},
			file: flat,
		},
	}
}

func ClassFor(template string) (Class, bool) {
	for _, c := range Classes() {
		if c.Template == template {
			return c, true
		}
	}
	return Class{}, false
}

// itemSchema is intentionally structural: required keys, types and sizes. Enum,
// single-line and evidence rules are enforced by the verifier so a single bad
// proposal is held with a reason instead of failing the whole node output.
func itemSchema(c Class) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for _, f := range c.Fields {
		properties[f.Name] = map[string]any{"type": "string", "maxLength": f.MaxBytes}
		if f.Required {
			required = append(required, f.Name)
		}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"id", "title", "body", "evidence_ids", "status", "attributes"}, "properties": map[string]any{
		"id":           map[string]any{"type": "string", "pattern": proposalID.String()},
		"title":        map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
		"body":         map[string]any{"type": "string", "minLength": 1, "maxLength": 16384},
		"evidence_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 100, "uniqueItems": true, "items": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}},
		"status":       map[string]any{"enum": []string{"supported", "held"}},
		"hold_reason":  map[string]any{"type": "string", "maxLength": 64},
		"attributes":   map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties},
	}}
}
func itemsSchema(c Class, max int) map[string]any {
	return map[string]any{"type": "array", "maxItems": max, "items": itemSchema(c)}
}
func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// Sensitive-content screening is a conservative backstop, not redaction. Held
// items stay visible in the run so a reviewer can rewrite them without secrets.
var sensitive = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|sk-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,})`),
	regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/-]{20,}`),
	regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret|token|password|passwd)\b["']?\s*[:=]\s*["']?[^\s"']{6,}`),
	regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`),
}

func containsSensitive(item Proposal) bool {
	texts := []string{item.Title, item.Body}
	for _, value := range item.Attributes {
		texts = append(texts, value)
	}
	for _, text := range texts {
		for _, pattern := range sensitive {
			if pattern.MatchString(text) {
				return true
			}
		}
	}
	return false
}
func singleLine(value string) bool { return !strings.ContainsAny(value, "\r\n") }
