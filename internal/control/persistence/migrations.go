package persistence

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

const (
	upMarker   = "-- +cable Up"
	downMarker = "-- +cable Down"
)

type Migration struct {
	Name string
	Up   string
	Down string
}

func LoadMigrations(filesystem fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(filesystem, ".")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	slices.Sort(names)
	migrations := make([]Migration, 0, len(names))
	for _, name := range names {
		content, err := fs.ReadFile(filesystem, name)
		if err != nil {
			return nil, err
		}
		migration, err := parseMigration(name, string(content))
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration)
	}
	if len(migrations) == 0 {
		return nil, errors.New("no SQL migrations found")
	}
	return migrations, nil
}

func parseMigration(name, content string) (Migration, error) {
	upIndex := strings.Index(content, upMarker)
	downIndex := strings.Index(content, downMarker)
	if upIndex != 0 || downIndex <= len(upMarker) {
		return Migration{}, fmt.Errorf("migration %s must contain ordered Up and Down markers", name)
	}
	up := strings.TrimSpace(content[len(upMarker):downIndex])
	down := strings.TrimSpace(content[downIndex+len(downMarker):])
	if up == "" || down == "" {
		return Migration{}, fmt.Errorf("migration %s has an empty direction", name)
	}
	return Migration{Name: name, Up: up, Down: down}, nil
}
