package memory

// Lenses are the extraction stage, adapted from the local memory-lab harness
// (which in turn adapts Mem0, WDM, Hermes, ReasoningBank, and AWM). Each lens is
// a system prompt plus the Go struct the model's JSON reply decodes into. This
// file holds the structs; the prompts live in prompts.yaml (see prompts.go) and
// are reachable as PromptSet.FactsPrompt / FailurePrompt / SkillPrompt. Each
// struct and its prompt are one contract: the prompt spells out the exact JSON
// shape decoded here.
//
// The three lens families map onto the three "agents" of the nightly loop:
//   - facts   → semantic memory (facts / preferences / heuristics)
//   - failure → episode + bug/correction memory (gated on a hard failure signal)
//   - skill   → reusable-workflow skill candidates (gated on turn count)

// ---- facts lens (Mem0 FACT_RETRIEVAL + WDM reusable gate) ----

// FactsResult decodes the facts lens reply.
type FactsResult struct {
	Memories []CandidateMemory `json:"memories"`
}

// CandidateMemory is one extracted semantic-memory candidate.
type CandidateMemory struct {
	Type        string  `json:"type"`
	Scope       string  `json:"scope"`
	Statement   string  `json:"statement"`
	WhenToApply string  `json:"when_to_apply"`
	Confidence  float64 `json:"confidence"`
}

// ---- failure lens (Hermes review + ReasoningBank FAILED_SI) ----

// FailureResult decodes the failure lens reply.
type FailureResult struct {
	Task            string            `json:"task"`
	Outcome         string            `json:"outcome"`
	UserFrustration string            `json:"user_frustration"`
	Lessons         []CandidateMemory `json:"lessons"`
}

// ---- skill lens (AWM workflow induction + ReasoningBank) ----

// SkillResult decodes the skill lens reply.
type SkillResult struct {
	Skills []CandidateSkill `json:"skills"`
}

// CandidateSkill is one induced reusable workflow.
type CandidateSkill struct {
	Name       string   `json:"name"`
	Scope      string   `json:"scope"`
	WhenToUse  string   `json:"when_to_use"`
	Steps      []string `json:"steps"`
	Pitfalls   []string `json:"pitfalls"`
	Confidence float64  `json:"confidence"`
}
