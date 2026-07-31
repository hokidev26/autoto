package db

import (
	"strings"
	"testing"
)

// TestStatementKeywordIgnoresLeadingComments guards the legacy upgrade path,
// which runs CREATE TABLE statements before CREATE INDEX ones by matching on a
// statement's prefix. A doc comment written above a table definition once hid
// that table from the CREATE TABLE pass while its indexes still ran, breaking
// every upgrade from a legacy database with "no such table".
func TestStatementKeywordIgnoresLeadingComments(t *testing.T) {
	for name, statement := range map[string]string{
		"single comment":   "-- scopes a memory\nCREATE TABLE IF NOT EXISTS memories (id TEXT)",
		"comment block":    "\n\n-- one\n-- two\n-- three\nCREATE TABLE IF NOT EXISTS memories (id TEXT)",
		"indented comment": "   -- one\n\t-- two\n\tCREATE TABLE IF NOT EXISTS memories (id TEXT)",
		"no comment":       "CREATE TABLE IF NOT EXISTS memories (id TEXT)",
	} {
		if got := statementKeyword(statement); !strings.HasPrefix(strings.ToUpper(got), "CREATE TABLE ") {
			t.Fatalf("%s: statement was not classified as CREATE TABLE: %q", name, got)
		}
	}
	if got := statementKeyword("-- trailing comment with no statement"); got != "" {
		t.Fatalf("comment-only chunk should classify as nothing, got %q", got)
	}
}

// TestLegacySchemaCreatesEveryIndexedTable is the invariant the comment bug
// broke: the legacy upgrade creates indexes from the same schema text it
// creates tables from, so any table an index references must survive the
// CREATE TABLE pass.
func TestLegacySchemaCreatesEveryIndexedTable(t *testing.T) {
	created := map[string]bool{}
	indexed := map[string]string{}
	for _, raw := range strings.Split(legacyNamingSchemaSQL(), ";") {
		stmt := statementKeyword(strings.TrimSpace(raw))
		upper := strings.ToUpper(stmt)
		switch {
		case strings.HasPrefix(upper, "CREATE TABLE "):
			if table := tableNameAfter(stmt, "CREATE TABLE IF NOT EXISTS "); table != "" {
				created[table] = true
			}
		case strings.HasPrefix(upper, "CREATE INDEX "), strings.HasPrefix(upper, "CREATE UNIQUE INDEX "):
			if _, target, found := strings.Cut(stmt, " ON "); found {
				name := strings.TrimSpace(target)
				if open := strings.IndexByte(name, '('); open > 0 {
					indexed[strings.TrimSpace(name[:open])] = firstLine(stmt)
				}
			}
		}
	}
	for table, index := range indexed {
		if !created[table] {
			t.Fatalf("legacy schema indexes %q but never creates it (%s); the upgrade fails with \"no such table\"", table, index)
		}
	}
}

func tableNameAfter(stmt, prefix string) string {
	rest := stmt
	if len(rest) >= len(prefix) && strings.EqualFold(rest[:len(prefix)], prefix) {
		rest = rest[len(prefix):]
	} else if _, after, found := strings.Cut(strings.ToUpper(stmt), "CREATE TABLE "); found {
		rest = stmt[len(stmt)-len(after):]
	}
	rest = strings.TrimSpace(rest)
	if open := strings.IndexAny(rest, " ("); open > 0 {
		rest = rest[:open]
	}
	return strings.TrimSpace(rest)
}
