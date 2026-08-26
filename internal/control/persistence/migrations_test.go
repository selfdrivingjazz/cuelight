package persistence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialMigrationHasReversibleDirections(t *testing.T) {
	root := filepath.Join("..", "..", "..", "migrations")
	migrations, err := LoadMigrations(os.DirFS(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 1 || migrations[0].Name != "0001_initial.sql" {
		t.Fatalf("unexpected migrations: %#v", migrations)
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
			t.Fatalf("migration missing %q", required)
		}
	}
}
