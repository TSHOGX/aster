package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness-platform/orchestrator/internal/memorystore"
)

func TestSummarizeGroupKeywordsIncludesScope(t *testing.T) {
	mems := []memorystore.SemanticMemory{
		{Scope: "Doris SQL", Statement: "Doris IN 子句值过多会超出表达式树上限，需分批查询或改用 JOIN 规避超限。"},
		{Scope: "Doris SQL", Statement: "JOIN 大表前先按 vid 和时间分区过滤。"},
	}
	got := summarizeGroupKeywords(mems)
	joined := strings.Join(got, "、")
	if !strings.Contains(joined, "Doris SQL") {
		t.Fatalf("keywords %v should start from the scope heading", got)
	}
	if !strings.Contains(joined, "JOIN") && !strings.Contains(joined, "Doris") {
		t.Fatalf("keywords %v should include a distinctive term from the statements", got)
	}
	if len(got) > maxGroupKeywords {
		t.Fatalf("keywords %v exceeds cap %d", got, maxGroupKeywords)
	}
}

func TestRenderIndexListsGroupKeywords(t *testing.T) {
	kb := NewKB(t.TempDir())
	mems := []memorystore.SemanticMemory{reviewMemory(2, "Doris SQL", "Doris IN 子句值过多需要分批查询。")}
	digest, err := kb.Publish(mems, nil)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	rawBytes, err := os.ReadFile(filepath.Join(kb.SnapshotDir(digest), "facts", "INDEX.md"))
	if err != nil {
		t.Fatalf("read INDEX: %v", err)
	}
	raw := string(rawBytes)
	if !strings.Contains(raw, "data-sources.md") && !strings.Contains(raw, "schema-fields.md") && !strings.Contains(raw, "other.md") {
		t.Fatalf("INDEX missing the facts file:\n%s", raw)
	}
	if !strings.Contains(raw, "Doris SQL") {
		t.Fatalf("INDEX should list the group keyword:\n%s", raw)
	}
}
