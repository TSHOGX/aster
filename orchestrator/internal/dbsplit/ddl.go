package dbsplit

import (
	"fmt"
	"regexp"
	"strings"
)

// This file does DDL surgery on the *live* schema text read back from
// sqlite_master, never on the base CREATE TABLE literals in store/schema.go.
// That matters because the store applies incremental `ALTER TABLE ... ADD
// COLUMN` migrations (native_id/parent_id on trajectory_events, the agentic
// columns on semantic_memories/skill_candidates, ...), and SQLite appends those
// columns to the stored DDL text. Reading sqlite_master therefore gives us the
// real, current column set on any given production file, including columns added
// by future migrations we have never heard of.

// sessionsFK is the fallback FK clause re-added by the reverse migration when
// the ledger's recorded source DDL is unavailable. The forward migration
// normally records the verbatim pre-migration DDL, which is authoritative; this
// exists so a rollback is still possible if the ledger was lost.
const sessionsFK = `FOREIGN KEY(session_id) REFERENCES sessions(id) ON DELETE CASCADE`

// tablesWithSessionFK lists the memory tables whose original DDL carries a FK
// into sessions(id). Cross-database foreign keys are impossible in SQLite, so
// these clauses are dropped on the memory.db side and restored on rollback.
var tablesWithSessionFK = map[string]bool{
	"trajectory_snapshots": true,
	"trajectory_events":    true,
	"extraction_state":     true,
	"episode_memories":     true,
	"raw_sources":          true,
}

// createTableParts is a CREATE TABLE statement decomposed around its
// column/constraint list, so the list can be rewritten and the statement
// reassembled without disturbing anything else.
type createTableParts struct {
	// Name is the table name as parsed out of the statement (unquoted).
	Name string
	// Body is the raw text between the outermost parentheses.
	Body string
	// Suffix is whatever follows the closing paren (e.g. WITHOUT ROWID, STRICT).
	Suffix string
}

// createTableRe matches a CREATE TABLE header. The optional leading
// `schema.` group means an already-qualified statement (as produced by
// render) can be re-parsed, not just the unqualified form sqlite_master
// stores; the table name is always the last identifier before the '('.
var createTableRe = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:TEMP\s+|TEMPORARY\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:` + identPattern + `\s*\.\s*)?` + identPattern + `\s*\(`)

// identPattern matches a SQLite identifier in any of its quoting forms.
const identPattern = `([A-Za-z_][A-Za-z0-9_$]*|"(?:[^"]|"")*"|\[[^\]]*\]|` + "`(?:[^`]|``)*`" + `)`

// parseCreateTable splits a CREATE TABLE statement into its name, its
// column/constraint list, and any trailing table options.
func parseCreateTable(ddl string) (createTableParts, error) {
	m := createTableRe.FindStringSubmatchIndex(ddl)
	if m == nil {
		return createTableParts{}, fmt.Errorf("not a CREATE TABLE statement: %.80q", ddl)
	}
	// Group 1 is the optional schema qualifier, group 2 the table name.
	name := unquoteIdent(ddl[m[4]:m[5]])
	open := m[1] - 1 // index of the '(' consumed by the regex
	closeIdx, err := matchParen(ddl, open)
	if err != nil {
		return createTableParts{}, fmt.Errorf("table %s: %w", name, err)
	}
	return createTableParts{
		Name:   name,
		Body:   ddl[open+1 : closeIdx],
		Suffix: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ddl[closeIdx+1:]), ";")),
	}, nil
}

// render reassembles a CREATE TABLE statement, qualified into the given schema
// ("main", "mem", ...) so it can be executed against an attached database.
func (p createTableParts) render(schema, body string) string {
	stmt := fmt.Sprintf("CREATE TABLE %s.%s (%s)", quoteIdent(schema), quoteIdent(p.Name), body)
	if p.Suffix != "" {
		stmt += " " + p.Suffix
	}
	return stmt
}

// stripSessionFKs returns the table's DDL with every table-level FOREIGN KEY
// clause targeting sessions(id) removed, plus the clauses it removed. All other
// constraints (PRIMARY KEY, CHECK, UNIQUE, NOT NULL, DEFAULT) are preserved
// verbatim.
//
// An inline column-level REFERENCES is rejected rather than stripped: the
// audited set has none, so encountering one means the schema changed under us
// and the operator should re-audit instead of silently losing a constraint.
func stripSessionFKs(ddl, schema string) (string, []string, error) {
	parts, err := parseCreateTable(ddl)
	if err != nil {
		return "", nil, err
	}
	clauses, err := splitTopLevel(parts.Body)
	if err != nil {
		return "", nil, fmt.Errorf("table %s: %w", parts.Name, err)
	}
	kept := make([]string, 0, len(clauses))
	var removed []string
	for _, clause := range clauses {
		trimmed := strings.TrimSpace(clause)
		if isForeignKeyClause(trimmed) {
			if !referencesSessions(trimmed) {
				return "", nil, fmt.Errorf("table %s: unexpected foreign key clause %q; re-audit the split before migrating", parts.Name, trimmed)
			}
			removed = append(removed, trimmed)
			continue
		}
		if hasInlineReferences(trimmed) {
			return "", nil, fmt.Errorf("table %s: column definition carries an inline REFERENCES (%q); re-audit the split before migrating", parts.Name, trimmed)
		}
		kept = append(kept, clause)
	}
	return parts.render(schema, strings.Join(kept, ",")), removed, nil
}

// restoreSessionFK re-adds the sessions(id) foreign key to a DDL statement that
// had it stripped. Used only as the rollback fallback when the ledger's recorded
// source DDL is missing.
func restoreSessionFK(ddl, schema string) (string, error) {
	parts, err := parseCreateTable(ddl)
	if err != nil {
		return "", err
	}
	clauses, err := splitTopLevel(parts.Body)
	if err != nil {
		return "", fmt.Errorf("table %s: %w", parts.Name, err)
	}
	for _, clause := range clauses {
		if isForeignKeyClause(strings.TrimSpace(clause)) && referencesSessions(strings.TrimSpace(clause)) {
			// Already present; nothing to restore.
			return parts.render(schema, parts.Body), nil
		}
	}
	return parts.render(schema, parts.Body+",\n  "+sessionsFK), nil
}

// requalifyCreateTable rewrites an unqualified CREATE TABLE (as stored in
// sqlite_master) so it targets a specific attached schema, leaving the
// column/constraint list untouched.
func requalifyCreateTable(ddl, schema string) (string, error) {
	parts, err := parseCreateTable(ddl)
	if err != nil {
		return "", err
	}
	return parts.render(schema, parts.Body), nil
}

var createIndexRe = regexp.MustCompile(`(?is)^(\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?)` + identPattern + `(\s+ON\s+)`)

// requalifyCreateIndex rewrites a CREATE INDEX statement so the index is created
// in the given schema. The table reference after ON is deliberately left alone:
// SQLite resolves it within the same schema as the index, which is exactly what
// we want.
//
// Reusing the stored DDL text (rather than rebuilding from pragma_index_info)
// preserves expression indexes, collations, DESC ordering and partial-index
// WHERE clauses verbatim.
func requalifyCreateIndex(ddl, schema string) (string, error) {
	m := createIndexRe.FindStringSubmatchIndex(ddl)
	if m == nil {
		return "", fmt.Errorf("not a CREATE INDEX statement: %.80q", ddl)
	}
	name := unquoteIdent(ddl[m[4]:m[5]])
	return ddl[m[2]:m[3]] + quoteIdent(schema) + "." + quoteIdent(name) + ddl[m[6]:], nil
}

func isForeignKeyClause(clause string) bool {
	upper := strings.ToUpper(clause)
	return strings.HasPrefix(upper, "FOREIGN KEY")
}

func referencesSessions(clause string) bool {
	// Normalize whitespace so "REFERENCES sessions (id)" and
	// "REFERENCES sessions(id)" both match.
	upper := strings.Join(strings.Fields(strings.ToUpper(clause)), " ")
	return strings.Contains(upper, "REFERENCES SESSIONS(ID)") ||
		strings.Contains(upper, "REFERENCES SESSIONS (ID)") ||
		strings.Contains(upper, `REFERENCES "SESSIONS"`)
}

// hasInlineReferences reports whether a column definition carries a
// column-level REFERENCES clause. Table-level constraint clauses are handled
// separately and must not reach here.
func hasInlineReferences(clause string) bool {
	upper := strings.ToUpper(clause)
	for _, kw := range []string{"PRIMARY KEY", "UNIQUE", "CHECK", "FOREIGN KEY"} {
		if strings.HasPrefix(upper, kw) {
			return false
		}
	}
	return containsKeywordOutsideParens(clause, "REFERENCES")
}

// containsKeywordOutsideParens looks for a bare keyword at paren depth zero,
// skipping quoted strings. Used so a CHECK(...) body or a DEFAULT string
// literal cannot produce a false positive.
func containsKeywordOutsideParens(s, keyword string) bool {
	upper := strings.ToUpper(s)
	kw := strings.ToUpper(keyword)
	depth := 0
	for i := 0; i < len(upper); i++ {
		switch c := upper[i]; c {
		case '\'', '"', '`':
			j := skipQuoted(upper, i, c)
			i = j
			continue
		case '[':
			depth++
		case ']':
			depth--
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth != 0 {
			continue
		}
		if strings.HasPrefix(upper[i:], kw) && isWordBoundary(upper, i-1) && isWordBoundary(upper, i+len(kw)) {
			return true
		}
	}
	return false
}

func isWordBoundary(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return true
	}
	c := s[i]
	return !(c == '_' || c == '$' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z'))
}

// splitTopLevel splits a CREATE TABLE body on commas at paren depth zero,
// respecting single-quoted string literals, double-quoted/bracketed/backticked
// identifiers, and nested parens. Nesting matters: a CHECK constraint such as
// CHECK(kind IN ('user','assistant')) contains commas that must not split.
func splitTopLevel(body string) ([]string, error) {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case '\'', '"', '`':
			j := skipQuoted(body, i, c)
			if j >= len(body) {
				return nil, fmt.Errorf("unterminated quote in DDL body")
			}
			i = j
		case '(', '[':
			depth++
		case ')', ']':
			depth--
			if depth < 0 {
				return nil, fmt.Errorf("unbalanced parentheses in DDL body")
			}
		case ',':
			if depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("unbalanced parentheses in DDL body")
	}
	out = append(out, body[start:])
	return out, nil
}

// skipQuoted returns the index of the closing quote for the quoted run starting
// at i, handling the doubled-quote escape form where the quote char is repeated.
func skipQuoted(s string, i int, quote byte) int {
	for j := i + 1; j < len(s); j++ {
		if s[j] != quote {
			continue
		}
		if j+1 < len(s) && s[j+1] == quote {
			j++
			continue
		}
		return j
	}
	return len(s)
}

// matchParen returns the index of the ')' matching the '(' at open.
func matchParen(s string, open int) (int, error) {
	if open < 0 || open >= len(s) || s[open] != '(' {
		return 0, fmt.Errorf("expected '(' at offset %d", open)
	}
	depth := 0
	for i := open; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'', '"', '`':
			i = skipQuoted(s, i, c)
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unbalanced parentheses")
}

func quoteIdent(v string) string {
	return `"` + strings.ReplaceAll(v, `"`, `""`) + `"`
}

func unquoteIdent(v string) string {
	if len(v) < 2 {
		return v
	}
	switch v[0] {
	case '"':
		return strings.ReplaceAll(v[1:len(v)-1], `""`, `"`)
	case '`':
		return strings.ReplaceAll(v[1:len(v)-1], "``", "`")
	case '[':
		return v[1 : len(v)-1]
	}
	return v
}
