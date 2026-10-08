package persistence

import (
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
	expected := []string{"0001_initial.sql", "0002_control_store.sql", "0003_cable.sql", "0004_programming.sql", "0005_cable_blocks.sql", "0006_remote_input.sql", "0007_media.sql", "0008_screenshot_jobs.sql", "0009_browser_nodes.sql", "0010_media_thumbnails.sql", "0011_web_items.sql", "0012_unified_media.sql", "0013_hosted_cable.sql", "0014_profiles.sql"}
	if len(migrations) != len(expected) {
		t.Fatalf("got %d migrations, want %d", len(migrations), len(expected))
	}
	for index, name := range expected {
		if migrations[index].Name != name {
			t.Fatalf("migration %d is %q, want %q", index, migrations[index].Name, name)
		}
		if migrations[index].Up == "" || migrations[index].Down == "" {
			t.Fatalf("migration %s has an empty direction", name)
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
