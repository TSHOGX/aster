package memory

import (
	"fmt"
	"strings"

	"harness-platform/orchestrator/internal/memorystore"
)

// A slice renders a normalized trajectory into a transcript string for a lens,
// deciding WHICH parts of the trace to feed. Borrowed from the memory-lab
// harness. Over-budget transcripts are sampled head+tail (not head-only) so a
// long agentic trace keeps its conclusions/outcome at the end.

const (
	// sliceCharBudget bounds a rendered transcript. Head+tail sampling keeps the
	// first headFrac and the remainder from the tail with an elision marker.
	sliceCharBudget = 12000
	headFrac        = 0.55
	// failureErrorThreshold: a session is a *material* failure only when its last
	// tool_result is an error OR it has at least this many tool_result errors. A
	// single recovered error (one grep miss) must not trip the failure lens.
	failureErrorThreshold = 2
)

// sliceDialogue renders user + assistant text only (no tool noise). Best for the
// facts lens: stable preferences/decisions live in what was said, not the tools.
func sliceDialogue(events []memorystore.TrajectoryEvent) string {
	var b strings.Builder
	for _, e := range events {
		switch e.Kind {
		case "user":
			writeTurn(&b, "用户", e.Text)
		case "assistant":
			writeTurn(&b, "助手", e.Text)
		}
	}
	return clamp(b.String())
}

// sliceAnnotated renders user + assistant narrative plus ONLY erroring
// tool_result events. Best for the failure lens: narrative + hard failure signal,
// with successful tool output (noise the assistant already narrates) dropped.
func sliceAnnotated(events []memorystore.TrajectoryEvent) string {
	var b strings.Builder
	for _, e := range events {
		switch e.Kind {
		case "user":
			writeTurn(&b, "用户", e.Text)
		case "assistant":
			writeTurn(&b, "助手", e.Text)
		case "tool_result":
			if e.IsError {
				writeTurn(&b, "工具错误["+e.Name+"]", e.Text)
			}
		}
	}
	return clamp(b.String())
}

// sliceReasoning renders user + thinking + tool_call (no results). Best for the
// skill lens: how the agent reasoned and which tools it invoked, in order, is
// what a reusable workflow is induced from.
func sliceReasoning(events []memorystore.TrajectoryEvent) string {
	var b strings.Builder
	for _, e := range events {
		switch e.Kind {
		case "user":
			writeTurn(&b, "用户", e.Text)
		case "thinking":
			writeTurn(&b, "思考", e.Text)
		case "tool_call":
			writeTurn(&b, "工具调用["+e.Name+"]", e.Text)
		}
	}
	return clamp(b.String())
}

func writeTurn(b *strings.Builder, label, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	fmt.Fprintf(b, "## %s\n%s\n\n", label, text)
}

// clamp keeps a transcript under budget by head+tail sampling.
func clamp(s string) string {
	if len(s) <= sliceCharBudget {
		return s
	}
	frac := headFrac
	head := int(float64(sliceCharBudget) * frac)
	tail := sliceCharBudget - head
	// Cut on rune boundaries by working with the byte slice conservatively.
	headPart := s[:head]
	tailPart := s[len(s)-tail:]
	return headPart + "\n\n…[已省略中间内容]…\n\n" + tailPart
}

// hasFailureSignal is the hard, zero-LLM gate for the failure lens. It returns
// true only on a *material* failure: the last tool_result is an error, or there
// are at least failureErrorThreshold tool_result errors. (Feedback-based signal
// is applied separately by the pipeline, which has access to message_feedback.)
func hasFailureSignal(events []memorystore.TrajectoryEvent) bool {
	errCount := 0
	lastToolResultErr := false
	for _, e := range events {
		if e.Kind != "tool_result" {
			continue
		}
		lastToolResultErr = e.IsError
		if e.IsError {
			errCount++
		}
	}
	return lastToolResultErr || errCount >= failureErrorThreshold
}

// countTurns returns the number of user events, a cheap proxy for turn count
// used by the skill lens gate.
func countUserTurns(events []memorystore.TrajectoryEvent) int {
	n := 0
	for _, e := range events {
		if e.Kind == "user" {
			n++
		}
	}
	return n
}
