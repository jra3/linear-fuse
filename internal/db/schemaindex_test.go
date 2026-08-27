package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// alterColumnRe matches the bootstrap migration's one statement shape:
// ALTER TABLE <table> ADD COLUMN <column> <type>.
var alterColumnRe = regexp.MustCompile(`(?i)ALTER\s+TABLE\s+([A-Za-z_]\w*)\s+ADD\s+COLUMN\s+([A-Za-z_]\w*)`)

// createIndexRe matches a CREATE INDEX statement and captures its name, table,
// and the raw parenthesised column list.
var createIndexRe = regexp.MustCompile(`(?is)CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_]\w*)\s+ON\s+([A-Za-z_]\w*)\s*\(([^)]*)\)`)

// TestSchemaIndexesAvoidMigratedColumns pins the rule that keeps an upgrade
// from deleting the user's cache: an index over a column migrateSchema ALTERs
// in must be created THERE, never in schema.sql.
//
// openDB executes schema.sql BEFORE migrateSchema, and CREATE TABLE IF NOT
// EXISTS leaves an existing table untouched. So on any database created before
// the column existed, an index over it fails the whole schema exec with "no
// such column" — which Open reads as "incompatible schema", answering by
// DELETING cache.db and recreating it. The migration never runs and the user
// resyncs their entire workspace from Linear.
//
// The failure is silent by construction: a fresh database (CI, every
// openTestStore) has the column from schema.sql, so nothing fails anywhere a
// normal test looks. Only a user with an existing cache pays, and they
// experience it as "the cache got slow once".
//
// idx_documents_team and idx_teams_parent both sat on this bug (#430, #432).
// TestMigrateAddsTeamParentID pins those two columns; this test pins the RULE,
// so the next ALTER-added column is caught when its index lands next to its
// table — which is where every other index in schema.sql lives, and therefore
// where the mistake will be made.
func TestSchemaIndexesAvoidMigratedColumns(t *testing.T) {
	t.Parallel()

	migrated, migrationIndexes := migrateSchemaStatements(t)

	// Vacuity guard: if the extraction stops matching, the test would pass by
	// finding nothing to check. migrateSchema has ALTERed at least one column
	// in since the migration landed.
	if len(migrated) == 0 {
		t.Fatal("no ALTER TABLE ... ADD COLUMN found in migrateSchema — the extraction has drifted from the code, not the other way round")
	}

	schemaIndexes := parseIndexes(t, schemaSQL)
	if len(schemaIndexes) < 10 {
		t.Fatalf("parsed %d CREATE INDEX statements out of schema.sql — the extraction has drifted (the file has dozens)", len(schemaIndexes))
	}

	for _, idx := range schemaIndexes {
		for _, col := range idx.columns {
			if migrated[idx.table][col] {
				t.Errorf("schema.sql creates %s over %s.%s, which migrateSchema ALTERs in.\n"+
					"On an upgraded cache the column does not exist when schema.sql runs: the exec fails\n"+
					"\"no such column\", Open deletes cache.db, and the user resyncs the whole workspace.\n"+
					"Move this index into migrateSchema's index loop, after the ALTERs.",
					idx.name, idx.table, col)
			}
		}
	}

	// The converse keeps the split honest: migrateSchema is where indexes over
	// ALTER-added columns go, not a second home for the index catalog.
	for _, idx := range migrationIndexes {
		covers := false
		for _, col := range idx.columns {
			if migrated[idx.table][col] {
				covers = true
				break
			}
		}
		if !covers {
			t.Errorf("migrateSchema creates %s over %s(%s), none of which it ALTERs in — "+
				"an index over a schema.sql column belongs in schema.sql, next to its table",
				idx.name, idx.table, strings.Join(idx.columns, ", "))
		}
	}
}

type indexDef struct {
	name    string
	table   string
	columns []string
}

// migrateSchemaStatements reads the SQL migrateSchema executes straight out of
// its source: the columns it ALTERs in, keyed table -> column, and the indexes
// it creates. Reading the function rather than a hand-kept list is the point —
// a new ALTER is covered the moment it is written.
func migrateSchemaStatements(t *testing.T) (map[string]map[string]bool, []indexDef) {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "store.go", nil, 0)
	if err != nil {
		t.Fatalf("parse store.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "migrateSchema" {
			fn = d
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatal("migrateSchema not found in store.go — if the migration moved, move this test with it")
	}

	migrated := map[string]map[string]bool{}
	var indexes []indexDef
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		sql := strings.Trim(lit.Value, "`\"")
		for _, m := range alterColumnRe.FindAllStringSubmatch(sql, -1) {
			table, column := strings.ToLower(m[1]), strings.ToLower(m[2])
			if migrated[table] == nil {
				migrated[table] = map[string]bool{}
			}
			migrated[table][column] = true
		}
		indexes = append(indexes, parseIndexes(t, sql)...)
		return true
	})
	return migrated, indexes
}

// parseIndexes extracts the CREATE INDEX statements from a chunk of SQL,
// lowercasing names and stripping the per-column ASC/DESC/COLLATE modifiers a
// column list may carry.
func parseIndexes(t *testing.T, sql string) []indexDef {
	t.Helper()

	var out []indexDef
	for _, m := range createIndexRe.FindAllStringSubmatch(sql, -1) {
		idx := indexDef{name: strings.ToLower(m[1]), table: strings.ToLower(m[2])}
		for _, part := range strings.Split(m[3], ",") {
			fields := strings.Fields(strings.TrimSpace(part))
			if len(fields) == 0 {
				continue
			}
			idx.columns = append(idx.columns, strings.ToLower(fields[0]))
		}
		out = append(out, idx)
	}
	return out
}
