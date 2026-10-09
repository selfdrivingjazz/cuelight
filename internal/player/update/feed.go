package update

import (
	"fmt"
	"strings"

	"github.com/jmilldotdev/cuelight/internal/updates"
)

// feedName maps a desktop platform to its electron-updater metadata file.
func feedName(goos, goarch string) (string, error) {
	switch {
	case goos == "darwin":
		return "latest-mac.yml", nil
	case goos == "windows":
		return "latest.yml", nil
	case goos == "linux" && goarch == "amd64":
		return "latest-linux.yml", nil
	case goos == "linux" && goarch == "arm64":
		return "latest-linux-arm64.yml", nil
	}
	return "", fmt.Errorf("no desktop update feed for %s/%s", goos, goarch)
}

// FeedArtifact returns this platform's electron-updater metadata file from a
// verified manifest.
func FeedArtifact(manifest updates.Manifest, goos, goarch string) (updates.Artifact, error) {
	name, err := feedName(goos, goarch)
	if err != nil {
		return updates.Artifact{}, err
	}
	if artifact, ok := findDesktop(manifest, name); ok {
		return artifact, nil
	}
	return updates.Artifact{}, fmt.Errorf("release %s has no %s", manifest.Version, name)
}

// FeedFiles returns every artifact the electron-updater metadata references
// (files[].url and path), plus each one's .blockmap when the release has one.
// Each referenced file must be listed in the signed manifest, since only those
// can be verified.
func FeedFiles(manifest updates.Manifest, feed []byte) ([]updates.Artifact, error) {
	names, err := feedReferences(feed)
	if err != nil {
		return nil, err
	}
	var files []updates.Artifact
	for _, name := range names {
		artifact, ok := findDesktop(manifest, name)
		if !ok {
			return nil, fmt.Errorf("update feed references %q, which the signed manifest does not list", name)
		}
		files = append(files, artifact)
		if blockmap, ok := findDesktop(manifest, name+".blockmap"); ok {
			files = append(files, blockmap)
		}
	}
	return files, nil
}

func findDesktop(manifest updates.Manifest, name string) (updates.Artifact, bool) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Component == updates.ComponentDesktop && artifact.Name == name {
			return artifact, true
		}
	}
	return updates.Artifact{}, false
}

// feedReferences extracts file names from electron-builder's latest*.yml:
// top-level `path:` and every `url:` inside `files:`. The format is fixed and
// machine-written, so a line scanner is sufficient.
func feedReferences(feed []byte) ([]string, error) {
	var names []string
	seen := make(map[string]bool)
	inFiles := false
	for _, line := range strings.Split(strings.ReplaceAll(string(feed), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		topLevel := line[0] != ' ' && line[0] != '\t' && line[0] != '-'
		if topLevel {
			inFiles = strings.HasPrefix(trimmed, "files:")
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(trimmed, "- "), ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if !(topLevel && key == "path") && !(inFiles && !topLevel && key == "url") {
			continue
		}
		name := unquote(strings.TrimSpace(value))
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("update feed references %q, which is not a plain file name", name)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("update feed references no files")
	}
	return names, nil
}

func unquote(value string) string {
	if len(value) >= 2 && (value[0] == '\'' || value[0] == '"') && value[len(value)-1] == value[0] {
		return value[1 : len(value)-1]
	}
	return value
}
