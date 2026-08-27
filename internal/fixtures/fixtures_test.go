package fixtures

import (
	"bytes"
	"errors"
	"testing"
)

func TestEmbeddedCatalogChecksumsSchemasAndNames(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateDeterminism(); err != nil {
		t.Fatal(err)
	}
	if catalog.Manifest.FixtureVersion != Version {
		t.Fatalf("fixture version = %q, want %q", catalog.Manifest.FixtureVersion, Version)
	}
	if len(catalog.Manifest.Media) != 6 {
		t.Fatalf("media count = %d, want 6", len(catalog.Manifest.Media))
	}
	for _, item := range catalog.Manifest.Media {
		if item.Name == "" || item.Path == "" || item.MediaType == "" || item.Size <= 0 || item.SHA256 == "" {
			t.Fatalf("incomplete manifest entry: %#v", item)
		}
		data, err := catalog.Bytes(item.Name)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(data)) != item.Size {
			t.Fatalf("%s size = %d, manifest says %d", item.Name, len(data), item.Size)
		}
		generated, err := Deterministic(item.Name)
		if errors.Is(err, ErrNotAlgorithmic) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, generated) {
			t.Fatalf("%s is not deterministic", item.Name)
		}
	}
}

func TestMediaCompatibilityAndCorruptionIdentities(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		kind  string
		valid bool
	}{
		"phase1.jpg": {"valid-image", true}, "phase1.png": {"valid-image", true}, "phase1.mp4": {"valid-video", true},
		"unsupported.webm": {"unsupported", true}, "corrupt.jpg": {"corrupt", false}, "truncated.mp4": {"truncated", false},
	}
	for name, expectation := range want {
		item, ok := catalog.Media(name)
		if !ok {
			t.Fatalf("missing media fixture %q", name)
		}
		if item.Kind != expectation.kind || item.Expected.Valid != expectation.valid {
			t.Fatalf("%s metadata = kind %q valid %v", name, item.Kind, item.Expected.Valid)
		}
	}
	good, _ := catalog.Bytes("phase1.jpg")
	bad, _ := catalog.Bytes("corrupt.jpg")
	if bytes.Equal(good, bad) {
		t.Fatal("corrupt fixture has the same bytes as its source")
	}
	full, _ := catalog.Bytes("phase1.mp4")
	partial, _ := catalog.Bytes("truncated.mp4")
	if len(partial) >= len(full) {
		t.Fatal("truncated fixture is not shorter")
	}
}

func TestTypedAPIFixtures(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range catalog.Manifest.API {
		fixture, err := catalog.API(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fixture.SchemaVersion != "1.0" || fixture.Operation == "" || fixture.Request.Method == "" || fixture.Response.Status == 0 {
			t.Fatalf("%s is not typed", name)
		}
	}
}

func TestTypedScenariosAndDeterministicCleanup(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range catalog.Manifest.Scenarios {
		scenario, err := catalog.Scenario(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if scenario.Name != name || len(scenario.Steps) == 0 || scenario.Expected.Cleanup != "deterministic" {
			t.Fatalf("%s is not a complete typed scenario", name)
		}
	}
}

func TestProvenanceAllowsRedistribution(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := catalog.Manifest.Provenance
	if p.License != "0BSD" || !p.RedistributionAllowed || p.Source == "" || p.Modifications == "" {
		t.Fatalf("incomplete provenance: %#v", p)
	}
}
