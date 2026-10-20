package examples

// Summary is the one-line purpose shown on example cards.
var summaries = map[string]string{
	"example-skills":      "Turn successful sessions into reusable SKILL.md procedures, with a critic that reviews each draft.",
	"example-corrections": "Capture lessons from failures and user corrections, each tied to an episode that shows the fix.",
	"example-facts":       "Extract durable facts, preferences, decisions and constraints, with scope and valid time.",
	"example-knowledge":   "Build a business semantic layer: entities, dimensions, metrics, relationships and terms.",
	"example-episodes":    "Write one reviewed case record per source episode: situation, actions, outcome and takeaway.",
}

// NodeDocs explains what each node of a built-in graph does, keyed by workflow ID
// and node ID. The editor shows it next to the node so a copy stays legible.
var nodeDocs = map[string]map[string]string{
	"example-skills": {
		"draft":  "Agent. Reads the corpus and drafts candidate skills from completed episodes. Starts with valid=false so the critic always reviews.",
		"revise": "Loop. Runs the critique workflow up to 2 times and stops once every proposal is marked valid; fails if it never converges.",
		"build":  "Builder. Re-checks evidence, rejects a changed corpus, and writes skills/<id>/SKILL.md. Held proposals stay in the run output.",
	},
	"example-skill-critique": {
		"critic": "Agent. Reviews each skill against a checklist (evidence, actionable steps, verification, overlap, secrets) and returns a revised state.",
	},
	"example-corrections": {
		"extract": "Agent. Finds mistakes (errors, wrong results, user corrections) and the completed episode that shows what worked instead.",
		"verify":  "Validator. Applies the evidence rule: a lesson needs at least one completed episode. Unsupported items are held with a reason.",
		"build":   "Builder. Writes lessons/<id>.md with trigger, mistaken assumption, corrective action and expected result.",
	},
	"example-facts": {
		"extract": "Agent. Extracts atomic claims that pass a four-question durability test: lasting, specific, useful later, self-contained.",
		"verify":  "Validator. Checks kinds, scope and supersession, counts support per source family and holds sensitive content.",
		"build":   "Builder. Writes facts/<id>.md with front matter for kind, scope, valid time and evidence.",
	},
	"example-knowledge": {
		"extract":     "Agent. Proposes entities, dimensions, metrics, relationships and terms that an authoritative source in the evidence states.",
		"verify":      "Validator. Requires complete metric definitions (formula, unit, grain, entity) and resolved evidence.",
		"consolidate": "Agent. Merges duplicate definitions under one ID and holds incompatible ones as conflicts. May not add evidence.",
		"check":       "Validator. Re-runs the evidence rule after merging, so the consolidator cannot invent support.",
		"build":       "Builder. Writes knowledge/<entities|dimensions|metrics|relationships|glossary>/<id>.md.",
	},
	"example-episodes": {
		"split":  "Builder. Picks one eligible episode per source family (skips truncated, unknown or copied ones) into a bounded batch.",
		"record": "Map. Runs the record workflow once per episode, 2 at a time. A failed item is collected instead of failing the batch.",
		"gather": "Builder. Assigns unique case IDs and turns failed recordings into held items with a reason.",
		"verify": "Validator. Requires exactly one source family and an outcome that matches the evidence.",
		"build":  "Builder. Writes episodes/<id>.md and lists skipped episodes as held output.",
	},
	"example-episode-record": {
		"record": "Agent. Writes one faithful case record for a single episode without generalizing.",
	},
}

func Summary(template string) string { return summaries[template] }

// NodeDocs returns a copy of the node explanations for one workflow ID.
func NodeDocs(workflow string) map[string]string {
	result := map[string]string{}
	for id, text := range nodeDocs[workflow] {
		result[id] = text
	}
	return result
}
