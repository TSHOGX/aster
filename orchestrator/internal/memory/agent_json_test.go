package memory

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
)

func TestExtractLastJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // "" means expect empty
	}{
		{"plain", `{"verdict":"current"}`, `{"verdict":"current"}`},
		{"fenced", "```json\n{\"verdict\":\"stale\"}\n```", `{"verdict":"stale"}`},
		{
			"prose-then-json",
			"Let me check the tree.\nI ran `ls sandbox-image/rootfs`.\nFinal answer:\n{\"verdict\":\"fixed\",\"rationale\":\"font now present\"}",
			`{"verdict":"fixed","rationale":"font now present"}`,
		},
		{
			"schema-example-then-answer",
			"I will output {\"verdict\":\"...\"} format. Here is my result: {\"verdict\":\"current\",\"corrected_statement\":null}",
			`{"verdict":"current","corrected_statement":null}`,
		},
		{
			"brace-in-string-prose",
			"The set {a,b} was checked. {\"verdict\":\"stale\",\"corrected_statement\":\"use /agents/skills\"}",
			`{"verdict":"stale","corrected_statement":"use /agents/skills"}`,
		},
		{"none", "no json here", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLastJSON(tc.in)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("expected empty, got %q", got)
				}
				return
			}
			// Compare structurally (whitespace-insensitive).
			var a, b any
			if err := json.Unmarshal([]byte(got), &a); err != nil {
				t.Fatalf("result not valid JSON: %q (%v)", got, err)
			}
			if err := json.Unmarshal([]byte(tc.want), &b); err != nil {
				t.Fatalf("want not valid JSON: %q", tc.want)
			}
			ga, _ := json.Marshal(a)
			gb, _ := json.Marshal(b)
			if string(ga) != string(gb) {
				t.Fatalf("got %s, want %s", ga, gb)
			}
		})
	}
}

func TestParMapOrderAndCoverage(t *testing.T) {
	items := make([]int, 100)
	for i := range items {
		items[i] = i
	}
	var calls int64
	got := parMap(context.Background(), items, func(_ context.Context, v int) int {
		atomic.AddInt64(&calls, 1)
		return v * 2
	})
	if calls != 100 {
		t.Fatalf("expected 100 calls, got %d", calls)
	}
	for i, v := range got {
		if v != i*2 {
			t.Fatalf("order broken at %d: got %d", i, v)
		}
	}
}

func TestParMapEmpty(t *testing.T) {
	got := parMap(context.Background(), []int{}, func(_ context.Context, v int) int { return v })
	if len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

// The fallback scan slices on brace depth alone, so it could hand back a prefix
// that is not valid JSON. Returning it moved the failure downstream, where verify
// logged "unmarshal verdict: invalid character 'C' after object" and dropped the
// item; returning "" makes it a shape error the runner retries instead.
func TestExtractLastJSONRejectsUnparseableFallback(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"trailing prose after object", `{"verdict":"current"} Continued analysis below.`},
		{"truncated object", `{"verdict":"current", "note": "cut off`},
		{"no object at all", `I could not determine the verdict.`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLastJSON(tc.in)
			if got != "" && !json.Valid([]byte(got)) {
				t.Fatalf("returned invalid JSON %q", got)
			}
		})
	}
}

// A well-formed object preceded by narration must still be found — the retry
// change must not make extraction stricter than it was.
func TestExtractLastJSONStillFindsObjectAfterProse(t *testing.T) {
	in := "Let me check the tree.\nI ran grep and found it.\n{\"verdict\":\"stale\",\"why\":\"path gone\"}"
	got := extractLastJSON(in)
	if !json.Valid([]byte(got)) || got != `{"verdict":"stale","why":"path gone"}` {
		t.Fatalf("extractLastJSON = %q", got)
	}
}
