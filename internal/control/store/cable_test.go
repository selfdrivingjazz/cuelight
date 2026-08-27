package store

import (
	"os"
	"strings"
	"testing"
)

func TestCableMigrationHasReversibleSchema(t *testing.T) {
	b, e := os.ReadFile("../../../migrations/0003_cable.sql")
	if e != nil {
		t.Fatal(e)
	}
	s := string(b)
	for _, marker := range []string{"-- +cable Up", "-- +cable Down", "CREATE TABLE cable_deployments", "CREATE TABLE cable_channels", "CREATE TABLE cable_program_entries", "CREATE TABLE cable_shell_builds", "CREATE TABLE cable_revisions", "DROP TABLE cable_revisions", "DROP TABLE cable_deployments"} {
		if !strings.Contains(s, marker) {
			t.Fatalf("migration missing %q", marker)
		}
	}
	if strings.Index(s, "-- +cable Up") > strings.Index(s, "-- +cable Down") {
		t.Fatal("migration markers reversed")
	}
}
