package memory

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed prompts.yaml
var embeddedPromptsYAML []byte

// PromptsPathEnv names the optional per-deployment prompt override file. It
// replaces individual prompts for one deployment without editing a tracked
// file, so a site whose scope taxonomy or house style is private can still run
// an unmodified release binary.
//
// Unlike the other optional config files there is no configDir-relative default
// path: the nightly job runs from cron with cwd `/` (see
// scripts/harness-kb-nightly.sh), and a guessed relative path there would
// resolve somewhere else and silently fall back to the embedded prompts. Env
// var or nothing means the two outcomes are "override" and "embedded", never
// "meant to override but didn't".
const PromptsPathEnv = "HARNESS_KB_PROMPTS_PATH"

// PromptSet holds the system prompt for every LLM node of the evolution loop.
//
// Grounding is prepended to all of them except Reflect, which plans the cycle
// rather than reading session records. Use the accessors rather than the fields
// so that composition stays in one place.
type PromptSet struct {
	Grounding string `yaml:"grounding"`

	Facts   string `yaml:"facts"`
	Failure string `yaml:"failure"`
	Skill   string `yaml:"skill"`

	Reflect string `yaml:"reflect"`

	BlockKey string `yaml:"block_key"`
	Cluster  string `yaml:"cluster"`
	Reduce   string `yaml:"reduce"`
	KBDedup  string `yaml:"kb_dedup"`

	Verify string `yaml:"verify"`

	MemoryCurate string `yaml:"memory_curate"`
	SkillCritic  string `yaml:"skill_critic"`

	Critic string `yaml:"critic"`
}

// fields maps each YAML key to its destination. Overrides are applied through
// this table rather than by decoding straight into the struct: yaml's zero
// value cannot tell "key absent" from "key present but empty", and an empty
// override has to be a hard error instead of a silent fall back to the default.
func (p *PromptSet) fields() map[string]*string {
	return map[string]*string{
		"grounding":     &p.Grounding,
		"facts":         &p.Facts,
		"failure":       &p.Failure,
		"skill":         &p.Skill,
		"reflect":       &p.Reflect,
		"block_key":     &p.BlockKey,
		"cluster":       &p.Cluster,
		"reduce":        &p.Reduce,
		"kb_dedup":      &p.KBDedup,
		"verify":        &p.Verify,
		"memory_curate": &p.MemoryCurate,
		"skill_critic":  &p.SkillCritic,
		"critic":        &p.Critic,
	}
}

// Grounded prompts: the shared clause, a blank line, then the body.
func compose(grounding, body string) string {
	return grounding + "\n\n" + body
}

// FactsPrompt is the A-harvest semantic-memory lens (decodes into FactsResult).
func (p PromptSet) FactsPrompt() string { return compose(p.Grounding, p.Facts) }

// FailurePrompt is the A-harvest failure lens (decodes into FailureResult).
func (p PromptSet) FailurePrompt() string { return compose(p.Grounding, p.Failure) }

// SkillPrompt is the A-harvest workflow-induction lens (decodes into SkillResult).
func (p PromptSet) SkillPrompt() string { return compose(p.Grounding, p.Skill) }

// ReflectPrompt is Node 0. It is the one prompt without the grounding clause.
func (p PromptSet) ReflectPrompt() string { return p.Reflect }

// BlockKeyPrompt is Node B0, scope-label canonicalization.
func (p PromptSet) BlockKeyPrompt() string { return compose(p.Grounding, p.BlockKey) }

// ClusterPrompt is Node B1, semantic clustering within one scope+type.
func (p PromptSet) ClusterPrompt() string { return compose(p.Grounding, p.Cluster) }

// ReducePrompt is Node B2, merging per-shard clusters.
func (p PromptSet) ReducePrompt() string { return compose(p.Grounding, p.Reduce) }

// KBDedupPrompt is Node B3, new cluster versus already-accepted memories.
func (p PromptSet) KBDedupPrompt() string { return compose(p.Grounding, p.KBDedup) }

// VerifyPrompt is Node C, read-only verification against the live tree.
func (p PromptSet) VerifyPrompt() string { return compose(p.Grounding, p.Verify) }

// MemoryCuratePrompt is Node D's memory gate.
func (p PromptSet) MemoryCuratePrompt() string { return compose(p.Grounding, p.MemoryCurate) }

// SkillCriticPrompt is the critic half of Node D's skill reflect-loop.
func (p PromptSet) SkillCriticPrompt() string { return compose(p.Grounding, p.SkillCritic) }

// CriticPrompt is Node F, the final cross-item consistency gate.
func (p PromptSet) CriticPrompt() string { return compose(p.Grounding, p.Critic) }

// Validate reports every prompt that is missing or blank. Callers run it on the
// embedded set too: "the committed defaults are complete" is then enforced on
// the data, not on the existence of a Go constant.
func (p PromptSet) Validate() error {
	var missing []string
	for key, dst := range p.fields() {
		if strings.TrimSpace(*dst) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("kb prompts: missing or empty: %s", strings.Join(missing, ", "))
}

// EmbeddedPrompts returns the prompts compiled into the binary, ignoring any
// override file.
func EmbeddedPrompts() (PromptSet, error) {
	var p PromptSet
	dec := yaml.NewDecoder(bytes.NewReader(embeddedPromptsYAML))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return PromptSet{}, fmt.Errorf("parse embedded prompts.yaml: %w", err)
	}
	if err := p.Validate(); err != nil {
		return PromptSet{}, err
	}
	return p, nil
}

// LoadPrompts returns the embedded prompts, with individual keys replaced by the
// optional file at PromptsPathEnv.
//
// An unset env var skips the override entirely and is not an error. A path that
// is set but unreadable, unparsable, names an unknown key, or supplies a blank
// body IS an error: a typo in a unit file has to fail loudly here, or it
// resurfaces weeks later as degraded consolidation quality that points at the
// model instead of at the typo.
func LoadPrompts() (PromptSet, error) {
	p, err := EmbeddedPrompts()
	if err != nil {
		return PromptSet{}, err
	}
	path := strings.TrimSpace(os.Getenv(PromptsPathEnv))
	if path == "" {
		return p, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return PromptSet{}, fmt.Errorf("read %s %s: %w", PromptsPathEnv, path, err)
	}
	var override map[string]string
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&override); err != nil {
		return PromptSet{}, fmt.Errorf("parse %s %s: %w", PromptsPathEnv, path, err)
	}
	dst := p.fields()
	known := make([]string, 0, len(dst))
	for key := range dst {
		known = append(known, key)
	}
	sort.Strings(known)
	for key, body := range override {
		target, ok := dst[key]
		if !ok {
			return PromptSet{}, fmt.Errorf("parse %s %s: unknown prompt %q (known: %s)", PromptsPathEnv, path, key, strings.Join(known, ", "))
		}
		if strings.TrimSpace(body) == "" {
			return PromptSet{}, fmt.Errorf("parse %s %s: prompt %q is empty; remove the key to keep the default", PromptsPathEnv, path, key)
		}
		*target = body
	}
	if err := p.Validate(); err != nil {
		return PromptSet{}, fmt.Errorf("%s %s: %w", PromptsPathEnv, path, err)
	}
	return p, nil
}
