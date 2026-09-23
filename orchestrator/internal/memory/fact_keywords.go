package memory

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"harness-platform/orchestrator/internal/memorystore"
)

const maxGroupKeywords = 6

var (
	latinKeyword = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_./+-]{1,}`)
	hanKeyword   = regexp.MustCompile(`\p{Han}{2,8}`)
)

var keywordStop = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "to": {}, "of": {}, "in": {},
	"on": {}, "for": {}, "with": {}, "from": {}, "that": {}, "this": {}, "when": {},
	"then": {}, "into": {}, "using": {}, "used": {}, "use": {}, "must": {}, "should": {},
	"need": {}, "needs": {}, "over": {}, "under": {}, "after": {}, "before": {},
	"about": {}, "fact": {}, "facts": {}, "heuristic": {}, "procedure": {},
	"preference": {}, "support": {}, "confidence": {}, "apply": {}, "true": {},
	"false": {}, "null": {}, "md": {},
	"需要": {}, "使用": {}, "进行": {}, "可以": {}, "如果": {}, "以及": {}, "或者": {},
	"一个": {}, "这个": {}, "不是": {}, "没有": {}, "不会": {}, "已经": {}, "然后": {},
	"时": {}, "中": {}, "对": {}, "将": {}, "把": {}, "被": {},
	"与": {}, "及": {}, "等": {}, "为": {}, "在": {}, "从": {}, "到": {}, "和": {},
	"或": {}, "并": {}, "且": {}, "的": {}, "了": {}, "是": {}, "会": {}, "要": {},
	"请": {}, "不": {}, "也": {}, "还": {}, "很": {}, "更": {}, "都": {}, "就": {},
	"查询": {}, "内容": {}, "问题": {}, "情况": {}, "方式": {}, "时候": {}, "之后": {},
	"之前": {}, "以上": {}, "以下": {}, "一条": {}, "两个": {}, "多个": {},
}

// summarizeGroupKeywords returns a short locator list for a facts file: scope
// headings first, then distinctive tokens from the statements. The INDEX uses
// this so a reviewer can tell the groups apart without opening every file.
func summarizeGroupKeywords(mems []memorystore.SemanticMemory) []string {
	var texts []string
	var scopes []string
	seenScope := map[string]struct{}{}
	for _, m := range mems {
		scope := strings.TrimSpace(m.Scope)
		if scope != "" && !strings.EqualFold(scope, "global") {
			if _, ok := seenScope[strings.ToLower(scope)]; !ok {
				seenScope[strings.ToLower(scope)] = struct{}{}
				scopes = append(scopes, scope)
			}
		}
		texts = append(texts, m.Statement, m.WhenToApply)
	}
	return mergeKeywords(scopes, extractKeywords(strings.Join(texts, "\n"), maxGroupKeywords))
}

// keywordsFromFactsFile extracts the same locator list from a rendered facts
// file, so a partial review can refresh INDEX.md without the original rows.
func keywordsFromFactsFile(text string) []string {
	parsed := parseFactsFile(text)
	var texts []string
	var scopes []string
	seenScope := map[string]struct{}{}
	for _, scope := range parsed.scopes {
		name := strings.TrimSpace(scope.name)
		if name != "" && !strings.EqualFold(name, "global") {
			if _, ok := seenScope[strings.ToLower(name)]; !ok {
				seenScope[strings.ToLower(name)] = struct{}{}
				scopes = append(scopes, name)
			}
		}
		for _, block := range scope.blocks {
			texts = append(texts, block.body)
		}
	}
	return mergeKeywords(scopes, extractKeywords(strings.Join(texts, "\n"), maxGroupKeywords))
}

func formatFactGroupTail(body string) string {
	tail := fmt.Sprintf(" — %d fact(s)", len(factIDs(body)))
	if kws := keywordsFromFactsFile(body); len(kws) > 0 {
		tail += " · " + strings.Join(kws, "、")
	}
	return tail
}

func mergeKeywords(scopes, tokens []string) []string {
	out := make([]string, 0, maxGroupKeywords)
	seen := map[string]struct{}{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || len(out) >= maxGroupKeywords {
			return
		}
		key := strings.ToLower(raw)
		if _, ok := seen[key]; ok || isKeywordStop(raw) {
			return
		}
		seen[key] = struct{}{}
		out = append(out, raw)
	}
	for _, scope := range scopes {
		add(scope)
	}
	for _, token := range tokens {
		add(token)
	}
	return out
}

func extractKeywords(text string, limit int) []string {
	if strings.TrimSpace(text) == "" || limit <= 0 {
		return nil
	}
	type scored struct {
		token string
		n     int
	}
	counts := map[string]int{}
	display := map[string]string{}
	add := func(raw string) {
		raw = strings.Trim(raw, ".,;:()[]{}<>\"'`")
		if isKeywordStop(raw) {
			return
		}
		key := strings.ToLower(raw)
		counts[key]++
		if _, ok := display[key]; !ok {
			display[key] = raw
		}
	}
	for _, m := range latinKeyword.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range hanKeyword.FindAllString(text, -1) {
		add(m)
	}
	ranked := make([]scored, 0, len(counts))
	for key, n := range counts {
		ranked = append(ranked, scored{token: display[key], n: n})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}
		return strings.ToLower(ranked[i].token) < strings.ToLower(ranked[j].token)
	})
	out := make([]string, 0, limit)
	for _, item := range ranked {
		out = append(out, item.token)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func isKeywordStop(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	if _, ok := keywordStop[strings.ToLower(raw)]; ok {
		return true
	}
	if _, ok := keywordStop[raw]; ok {
		return true
	}
	runes := []rune(raw)
	if len(runes) == 1 {
		return true
	}
	if len(runes) == 2 && unicode.IsLower(runes[0]) && unicode.IsLower(runes[1]) {
		return true
	}
	letters := 0
	for _, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			letters++
		}
	}
	return letters == 0
}
