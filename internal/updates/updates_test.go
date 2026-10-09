package updates

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func testManifest() Manifest {
	return Manifest{
		Schema:                 SchemaV1,
		Product:                Product,
		Version:                "0.2.0",
		Channel:                ChannelStable,
		PublishedAt:            time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC),
		MinimumProtocolVersion: "1.0",
		Artifacts: []Artifact{{
			Component: ComponentDesktop, OS: "darwin", Arch: "arm64", Format: "zip",
			Name: "Cuelight-0.2.0-mac-arm64.zip", Size: 10, SHA256: strings.Repeat("ab", 32),
		}},
	}
}

func testKeys(t *testing.T) (ed25519.PrivateKey, map[string]ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private, map[string]ed25519.PublicKey{"test": public}
}

func TestSignedManifestVerifies(t *testing.T) {
	private, trusted := testKeys(t)
	envelope, err := Sign(testManifest(), "test", private)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := envelope.Verify(trusted)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest.Find(ComponentDesktop, "darwin", "arm64", "zip"); !ok {
		t.Fatal("signed artifact missing after verification")
	}
}

func TestTamperedManifestIsRejected(t *testing.T) {
	private, trusted := testKeys(t)
	envelope, err := Sign(testManifest(), "test", private)
	if err != nil {
		t.Fatal(err)
	}
	envelope.Manifest = []byte(strings.Replace(string(envelope.Manifest), strings.Repeat("ab", 32), strings.Repeat("cd", 32), 1))
	if _, err := envelope.Verify(trusted); err == nil {
		t.Fatal("tampered manifest verified")
	}
}

func TestUntrustedKeyIsRejected(t *testing.T) {
	private, _ := testKeys(t)
	_, otherTrusted := testKeys(t)
	envelope, err := Sign(testManifest(), "test", private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := envelope.Verify(otherTrusted); err == nil {
		t.Fatal("manifest signed by another key verified")
	}
	envelope.KeyID = "unknown"
	if _, err := envelope.Verify(otherTrusted); err == nil {
		t.Fatal("manifest with unknown key id verified")
	}
}

func TestContentReleaseSignatureCannotPassAsManifest(t *testing.T) {
	private, trusted := testKeys(t)
	envelope, err := Sign(testManifest(), "test", private)
	if err != nil {
		t.Fatal(err)
	}
	// A signature over the raw bytes (as another signing purpose would make)
	// must not verify as a release manifest.
	envelope.Signature = ed25519.Sign(private, envelope.Manifest)
	if _, err := envelope.Verify(trusted); err == nil {
		t.Fatal("signature without the release context verified")
	}
}

func TestInvalidManifestsAreNotSigned(t *testing.T) {
	private, _ := testKeys(t)
	cases := map[string]func(*Manifest){
		"path traversal name": func(m *Manifest) { m.Artifacts[0].Name = "../evil" },
		"uppercase digest":    func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("AB", 32) },
		"bad version":         func(m *Manifest) { m.Version = "1.2" },
		"duplicate target": func(m *Manifest) {
			duplicate := m.Artifacts[0]
			duplicate.Name = "other.zip"
			m.Artifacts = append(m.Artifacts, duplicate)
		},
		"unknown channel": func(m *Manifest) { m.Channel = "nightly" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := testManifest()
			manifest.Artifacts = append([]Artifact(nil), manifest.Artifacts...)
			mutate(&manifest)
			if _, err := Sign(manifest, "test", private); err == nil {
				t.Fatal("invalid manifest signed")
			}
		})
	}
}

func TestVersionPrecedence(t *testing.T) {
	ordered := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0"}
	for i := 0; i+1 < len(ordered); i++ {
		c, err := CompareVersions(ordered[i], ordered[i+1])
		if err != nil {
			t.Fatal(err)
		}
		if c != -1 {
			t.Fatalf("%s should precede %s", ordered[i], ordered[i+1])
		}
	}
	for _, invalid := range []string{"01.0.0", "1.0", "v1.0.0", "1.0.0-", "1.0.0-a..b"} {
		if _, err := ParseVersion(invalid); err == nil {
			t.Fatalf("%q parsed", invalid)
		}
	}
}

func TestCompiledKeysDecode(t *testing.T) {
	if len(TrustedKeys()) == 0 {
		t.Fatal("no compiled release keys")
	}
}
