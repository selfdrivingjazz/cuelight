package persistence

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationsHaveOrderedReversibleDirections(t *testing.T) {
	root := filepath.Join("..", "..", "..", "migrations")
	migrations, err := LoadMigrations(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 15 {
		t.Fatalf("got %d migrations, want at least 15", len(migrations))
	}
	for index, migration := range migrations {
		if prefix := fmt.Sprintf("%04d_", index+1); !strings.HasPrefix(migration.Name, prefix) || !strings.HasSuffix(migration.Name, ".sql") {
			t.Fatalf("migration %d is %q, want %s*.sql", index, migration.Name, prefix)
		}
		if migration.Up == "" || migration.Down == "" {
			t.Fatalf("migration %s has an empty direction", migration.Name)
		}
	}
	for _, required := range []string{
		"CREATE TABLE nodes",
		"CREATE TABLE releases",
		"CREATE TABLE assignments",
		"CREATE TABLE node_release_authorizations",
		"DROP TABLE node_release_authorizations",
		"DROP TABLE nodes",
	} {
		if !strings.Contains(migrations[0].Up+"\n"+migrations[0].Down, required) {
			t.Fatalf("initial migration missing %q", required)
		}
	}
}
