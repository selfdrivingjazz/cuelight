// Command cuelight-release builds, signs, and verifies Cuelight release manifests.
//
//	cuelight-release manifest --version V [--channel stable] --dir DIST --out cuelight-release.json
//	cuelight-release verify --manifest cuelight-release.json [--dir DIST]
//
// manifest signs with the PKCS#8 PEM Ed25519 key in $CUELIGHT_RELEASE_SIGNING_KEY
// (PEM text) or --key-file. Every file in DIST must match a known artifact name;
// unknown files are an error so nothing ships unsigned by accident.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/updates"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: cuelight-release manifest|verify [flags]")
		return 2
	}
	var err error
	switch args[0] {
	case "manifest":
		err = runManifest(args[1:], getenv, stdout, stderr)
	case "verify":
		err = runVerify(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "cuelight-release: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "cuelight-release: %v\n", err)
		return 1
	}
	return 0
}

func runManifest(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("manifest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	version := flags.String("version", "", "release version (MAJOR.MINOR.PATCH[-PRERELEASE])")
	channel := flags.String("channel", updates.ChannelStable, "release channel (stable|beta)")
	dir := flags.String("dir", "", "directory holding every release artifact")
	out := flags.String("out", "", "output path for the signed envelope")
	keyID := flags.String("key-id", "cuelight-2026a", "release signing key id")
	keyFile := flags.String("key-file", "", "PEM key file (default: $CUELIGHT_RELEASE_SIGNING_KEY)")
	minimumProtocol := flags.String("minimum-protocol", "1.0", "minimum control protocol version")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *version == "" || *dir == "" || *out == "" {
		return errors.New("manifest requires --version, --dir, and --out")
	}
	keyPEM := []byte(getenv("CUELIGHT_RELEASE_SIGNING_KEY"))
	if *keyFile != "" {
		var err error
		if keyPEM, err = os.ReadFile(*keyFile); err != nil {
			return err
		}
	}
	if len(keyPEM) == 0 {
		return errors.New("no signing key: set CUELIGHT_RELEASE_SIGNING_KEY or --key-file")
	}
	key, err := updates.ParsePrivateKey(keyPEM)
	if err != nil {
		return err
	}
	artifacts, err := collect(*dir, *version, filepath.Base(*out))
	if err != nil {
		return err
	}
	manifest := updates.Manifest{
		Schema:                 updates.SchemaV1,
		Product:                updates.Product,
		Version:                *version,
		Channel:                *channel,
		PublishedAt:            time.Now().UTC().Truncate(time.Second),
		MinimumProtocolVersion: *minimumProtocol,
		Artifacts:              artifacts,
	}
	envelope, err := updates.Sign(manifest, *keyID, key)
	if err != nil {
		return err
	}
	if _, err := envelope.Verify(updates.TrustedKeys()); err != nil {
		return fmt.Errorf("signed manifest does not verify against compiled release keys: %w", err)
	}
	document, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(document, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "signed %s %s with %d artifacts\n", updates.Product, *version, len(artifacts))
	return nil
}

func runVerify(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("manifest", "", "signed envelope path")
	dir := flags.String("dir", "", "optional directory to check artifact sizes and digests")
	if err := flags.Parse(args); err != nil {
		return err
	}
	document, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	envelope, err := updates.ParseEnvelope(document)
	if err != nil {
		return err
	}
	manifest, err := envelope.Verify(updates.TrustedKeys())
	if err != nil {
		return err
	}
	if *dir != "" {
		for _, artifact := range manifest.Artifacts {
			size, digest, err := hashFile(filepath.Join(*dir, artifact.Name))
			if err != nil {
				return err
			}
			if size != artifact.Size || digest != artifact.SHA256 {
				return fmt.Errorf("%s does not match the signed manifest", artifact.Name)
			}
		}
	}
	fmt.Fprintf(stdout, "verified %s %s (%s, key %s, %d artifacts)\n", manifest.Product, manifest.Version, manifest.Channel, envelope.KeyID, len(manifest.Artifacts))
	return nil
}

type classifier struct {
	pattern  *regexp.Regexp
	classify func(match []string) (component, os, arch, format string)
}

var (
	goComponents = map[string]string{
		"cuelight-control":      updates.ComponentControl,
		"cuelight-player":       updates.ComponentPlayer,
		"cuelight-minimal-node": updates.ComponentMinimalNode,
		"cuelight":              updates.ComponentCLI,
	}
	desktopOS   = map[string]string{"mac": "darwin", "win": "windows", "linux": "linux"}
	desktopArch = map[string]string{"x64": "amd64", "x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64", "universal": "universal"}
	desktopFmt  = map[string]string{"zip": "zip", "dmg": "dmg", "exe": "nsis", "AppImage": "appimage", "deb": "deb", "tar.gz": "tar.gz"}
	updaterYML  = map[string][2]string{
		"latest.yml":             {"windows", "any"},
		"latest-mac.yml":         {"darwin", "any"},
		"latest-linux.yml":       {"linux", "amd64"},
		"latest-linux-arm64.yml": {"linux", "arm64"},
	}
)

func classifiers(version string) []classifier {
	v := regexp.QuoteMeta(version)
	return []classifier{
		{regexp.MustCompile(`^(cuelight-control|cuelight-player|cuelight-minimal-node|cuelight)_` + v + `_(linux|darwin|windows)_(amd64|arm64)\.(tar\.gz|zip)$`),
			func(m []string) (string, string, string, string) { return goComponents[m[1]], m[2], m[3], m[4] }},
		{regexp.MustCompile(`^(cuelight-control|cuelight-player|cuelight-minimal-node)_` + v + `_(amd64|arm64)\.deb$`),
			func(m []string) (string, string, string, string) { return goComponents[m[1]], "linux", m[2], "deb" }},
		{regexp.MustCompile(`^Cuelight-` + v + `-(mac|win|linux)-(x64|x86_64|amd64|arm64|aarch64|universal)\.(zip|dmg|exe|AppImage|deb|tar\.gz)(\.blockmap)?$`),
			func(m []string) (string, string, string, string) {
				return updates.ComponentDesktop, desktopOS[m[1]], desktopArch[m[2]], desktopFmt[m[3]] + m[4]
			}},
		{regexp.MustCompile(`^(latest|latest-mac|latest-linux|latest-linux-arm64)\.yml$`),
			func(m []string) (string, string, string, string) {
				target := updaterYML[m[0]]
				return updates.ComponentDesktop, target[0], target[1], "yml"
			}},
		{regexp.MustCompile(`^Cuelight-` + v + `-android\.apk$`),
			func([]string) (string, string, string, string) {
				return updates.ComponentAndroid, "android", "any", "apk"
			}},
	}
}

func collect(dir, version, skip string) ([]updates.Artifact, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	rules := classifiers(version)
	var artifacts []updates.Artifact
	var unknown []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == skip || entry.Name() == updates.ManifestFileName {
			continue
		}
		name := entry.Name()
		var artifact *updates.Artifact
		for _, rule := range rules {
			if match := rule.pattern.FindStringSubmatch(name); match != nil {
				component, osName, arch, format := rule.classify(match)
				artifact = &updates.Artifact{Component: component, OS: osName, Arch: arch, Format: format, Name: name}
				break
			}
		}
		if artifact == nil {
			unknown = append(unknown, name)
			continue
		}
		size, digest, err := hashFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		artifact.Size, artifact.SHA256 = size, digest
		artifacts = append(artifacts, *artifact)
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unrecognized release files: %s", strings.Join(unknown, ", "))
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Name < artifacts[j].Name })
	return artifacts, nil
}

func hashFile(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}
