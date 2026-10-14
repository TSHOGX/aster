package evolution

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictWorkflowDecode(t *testing.T) {
	raw, err := json.Marshal(simpleWorkflow())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWorkflow(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Replace(string(raw), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1), strings.Replace(string(raw), `"schema_version":1`, `"schema_version":1,"unknown":true`, 1), string(raw) + `{}`, strings.Replace(string(raw), `"type":"object"`, `"type":"object","type":"object"`, 1)} {
		if _, err := DecodeWorkflow(strings.NewReader(invalid)); err == nil {
			t.Fatal("ambiguous document accepted")
		}
	}
}
func TestEvaluationIdentity(t *testing.T) {
	rev := Revision{"profile", 1, Digest([]byte("v1"))}
	valid := []EvaluationPolicy{{Mode: "disabled", ReferenceStrategy: "none"}, {Mode: "report_only", Profile: &rev, Service: &rev, ReferenceStrategy: "generation_base"}, {Mode: "required", Profile: &rev, Service: &rev, ReferenceStrategy: "target_main"}}
	for _, p := range valid {
		if err := p.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	invalid := []EvaluationPolicy{{Mode: "disabled", Profile: &rev, ReferenceStrategy: "none"}, {Mode: "required", Profile: &rev, Service: &rev, ReferenceStrategy: "baseline"}, {Mode: "required", Profile: &rev, Service: &rev, ReferenceStrategy: "pinned_release"}}
	for _, p := range invalid {
		if p.Validate() == nil {
			t.Fatal("ambiguous evaluation accepted")
		}
	}
}
func TestPatchPrefixAndArtifact(t *testing.T) {
	output := OutputBinding{"library", "skills", "authored-v1", 1}
	base := ContentRef{"library", strings.Repeat("a", 64), "authored-v1", Digest([]byte("tree"))}
	artifact := ArtifactRef{"artifact", "alpha", Digest([]byte("schema")), Digest([]byte("bytes")), 5, "text/plain", "grant", 1}
	patch := Patch{"run", 1, base, 1, []PatchEntry{{"put", "skills/file", "file", &artifact}}}
	if err := patch.Validate(output); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"skills-other/file", "skills/../outside", "outside", "/skills/file"} {
		bad := patch
		bad.Entries = []PatchEntry{{"put", p, "file", &artifact}}
		if bad.Validate(output) == nil {
			t.Fatalf("admitted %s", p)
		}
	}
	artifact.GrantID = ""
	if patch.Validate(output) == nil {
		t.Fatal("unscoped artifact accepted")
	}
}

func TestWireRequiresExactFields(t *testing.T) {
	raw, err := json.Marshal(simpleWorkflow())
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Replace(string(raw), `"schema_version":1,`, "", 1), strings.Replace(string(raw), `"schema_version":1`, `"SCHEMA_VERSION":1`, 1), strings.Replace(string(raw), `"max_attempts":2`, `"max_attempts":null`, 1)} {
		if _, err := DecodeWorkflow(strings.NewReader(invalid)); err == nil {
			t.Fatal("ambiguous wire fields accepted")
		}
	}
}
