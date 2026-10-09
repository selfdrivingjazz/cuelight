package software

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jmilldotdev/cuelight/internal/media/localcas"
	"github.com/jmilldotdev/cuelight/internal/player/client"
	"github.com/jmilldotdev/cuelight/internal/updates"
)

// Blobs is the control CAS subset used for release artifacts.
type Blobs interface {
	Put(io.Reader, string) (localcas.Object, error)
	Stat(string) (localcas.Object, error)
	Open(string) (*os.File, error)
}

// Service imports releases and manages node software targets.
type Service struct {
	Repository Repository
	Blobs      Blobs
	// Trusted overrides the compiled-in vendor keys; tests only.
	Trusted map[string]ed25519.PublicKey
	// PublicURL makes player-facing URLs absolute; empty yields
	// origin-relative URLs.
	PublicURL string
	// ControlVersion is the version of this control binary.
	ControlVersion string
	Feed           Feed
	Logger         *slog.Logger
}

func (s *Service) trusted() map[string]ed25519.PublicKey {
	if s.Trusted != nil {
		return s.Trusted
	}
	return updates.TrustedKeys()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// ArtifactImport is the outcome for one manifest artifact.
type ArtifactImport struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// ImportResult summarizes one import.
type ImportResult struct {
	Version   string           `json:"version"`
	Created   bool             `json:"created"`
	Artifacts []ArtifactImport `json:"artifacts"`
}

// Importer copies the artifacts of one verified release into CAS.
type Importer struct {
	service  *Service
	release  Release
	created  bool
	byName   map[string]updates.Artifact
	ready    map[string]bool
	outcomes map[string]ArtifactImport
}

// BeginImport verifies a signed envelope document with the trusted vendor keys
// and records the release. Artifacts are added with PutArtifact.
func (s *Service) BeginImport(ctx context.Context, source string, document []byte) (*Importer, error) {
	envelope, err := updates.ParseEnvelope(document)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	manifest, err := envelope.Verify(s.trusted())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnverified, err)
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	release := Release{
		Version: manifest.Version, Channel: manifest.Channel, PublishedAt: manifest.PublishedAt,
		MinimumProtocolVersion: manifest.MinimumProtocolVersion, KeyID: envelope.KeyID,
		Envelope: canonical, Source: source,
	}
	for _, artifact := range manifest.Artifacts {
		release.Artifacts = append(release.Artifacts, Artifact{Artifact: artifact})
	}
	created, err := s.Repository.PutSoftwareRelease(ctx, release)
	if err != nil {
		return nil, err
	}
	stored, err := s.Repository.GetSoftwareRelease(ctx, release.Version)
	if err != nil {
		return nil, err
	}
	importer := &Importer{service: s, release: stored, created: created, byName: map[string]updates.Artifact{}, ready: map[string]bool{}, outcomes: map[string]ArtifactImport{}}
	for _, artifact := range stored.Artifacts {
		importer.byName[artifact.Name] = artifact.Artifact
		if artifact.Ready {
			if object, err := s.Blobs.Stat(artifact.SHA256); err == nil && object.Size == artifact.Size {
				importer.ready[artifact.Name] = true
			}
		}
	}
	return importer, nil
}

// Version is the release being imported.
func (i *Importer) Version() string { return i.release.Version }

// Needs reports whether the named manifest artifact still has to be copied.
func (i *Importer) Needs(name string) bool {
	_, listed := i.byName[name]
	return listed && !i.ready[name]
}

// Artifacts lists the manifest artifacts in manifest order.
func (i *Importer) Artifacts() []updates.Artifact {
	artifacts := make([]updates.Artifact, 0, len(i.release.Artifacts))
	for _, artifact := range i.release.Artifacts {
		artifacts = append(artifacts, artifact.Artifact)
	}
	return artifacts
}

// PutArtifact stores the named artifact's bytes when they match the signed
// sha256 and size. Read failures are returned without marking the artifact
// rejected so that a retry can still import it.
func (i *Importer) PutArtifact(ctx context.Context, name string, body io.Reader) error {
	artifact, ok := i.byName[name]
	if !ok {
		return fmt.Errorf("%w: %q is not an artifact of release %s", ErrInvalid, name, i.release.Version)
	}
	if i.ready[name] {
		_, err := io.Copy(io.Discard, body)
		i.outcomes[name] = ArtifactImport{Name: name, State: ArtifactPresent}
		return err
	}
	tracked := &trackingReader{reader: io.LimitReader(body, artifact.Size+1)}
	object, err := i.service.Blobs.Put(tracked, artifact.SHA256)
	if err != nil {
		if tracked.err != nil || !tracked.eof {
			return fmt.Errorf("read artifact %s: %w", name, errors.Join(tracked.err, err))
		}
		i.outcomes[name] = ArtifactImport{Name: name, State: ArtifactRejected, Error: fmt.Sprintf("sha256/size do not match the manifest (%d bytes expected)", artifact.Size)}
		return fmt.Errorf("%w: %s", ErrArtifactMismatch, name)
	}
	if object.Size != artifact.Size {
		i.outcomes[name] = ArtifactImport{Name: name, State: ArtifactRejected, Error: "size does not match the manifest"}
		return fmt.Errorf("%w: %s", ErrArtifactMismatch, name)
	}
	if err := i.service.Repository.MarkSoftwareArtifactReady(ctx, i.release.Version, name); err != nil {
		return err
	}
	i.ready[name] = true
	i.outcomes[name] = ArtifactImport{Name: name, State: ArtifactImported}
	return nil
}

// Finish reports every artifact's outcome; it fails when any was rejected.
func (i *Importer) Finish() (ImportResult, error) {
	result := ImportResult{Version: i.release.Version, Created: i.created}
	var rejected []string
	for _, artifact := range i.release.Artifacts {
		outcome, ok := i.outcomes[artifact.Name]
		switch {
		case ok:
		case i.ready[artifact.Name]:
			outcome = ArtifactImport{Name: artifact.Name, State: ArtifactPresent}
		default:
			outcome = ArtifactImport{Name: artifact.Name, State: ArtifactMissing}
		}
		if outcome.State == ArtifactRejected {
			rejected = append(rejected, artifact.Name)
		}
		result.Artifacts = append(result.Artifacts, outcome)
	}
	if len(rejected) > 0 {
		return result, fmt.Errorf("%w: %s", ErrArtifactMismatch, strings.Join(rejected, ", "))
	}
	return result, nil
}

// ImportDir imports dir/cuelight-release.json and every artifact it lists
// that is present in dir.
func (s *Service) ImportDir(ctx context.Context, source, dir string) (ImportResult, error) {
	document, err := os.ReadFile(filepath.Join(dir, updates.ManifestFileName))
	if err != nil {
		return ImportResult{}, fmt.Errorf("read release envelope: %w", err)
	}
	importer, err := s.BeginImport(ctx, source, document)
	if err != nil {
		return ImportResult{}, err
	}
	for _, artifact := range importer.Artifacts() {
		if !importer.Needs(artifact.Name) {
			continue
		}
		file, err := os.Open(filepath.Join(dir, artifact.Name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return ImportResult{}, err
		}
		err = importer.PutArtifact(ctx, artifact.Name, file)
		file.Close()
		if err != nil && !errors.Is(err, ErrArtifactMismatch) {
			return ImportResult{}, err
		}
	}
	return importer.Finish()
}

type trackingReader struct {
	reader io.Reader
	eof    bool
	err    error
}

func (r *trackingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.eof = true
	} else if err != nil {
		r.err = err
	}
	return n, err
}

// Releases lists imported releases, newest version first.
func (s *Service) Releases(ctx context.Context) ([]Release, error) {
	releases, err := s.Repository.ListSoftwareReleases(ctx)
	if err != nil {
		return nil, err
	}
	sortNewestFirst(releases)
	return releases, nil
}

func sortNewestFirst(releases []Release) {
	sort.SliceStable(releases, func(a, b int) bool {
		order, err := updates.CompareVersions(releases[a].Version, releases[b].Version)
		return err == nil && order > 0
	})
}

// Release returns one imported release.
func (s *Service) Release(ctx context.Context, version string) (Release, error) {
	return s.Repository.GetSoftwareRelease(ctx, version)
}

// Envelope returns the signed envelope document of an imported release.
func (s *Service) Envelope(ctx context.Context, version string) ([]byte, error) {
	release, err := s.Repository.GetSoftwareRelease(ctx, version)
	if err != nil {
		return nil, err
	}
	return release.Envelope, nil
}

// OpenArtifact opens the CAS bytes of a ready artifact by sha256.
func (s *Service) OpenArtifact(ctx context.Context, sha256 string) (*os.File, error) {
	if localcas.ValidateDigest(sha256) != nil {
		return nil, ErrNotFound
	}
	ready, err := s.Repository.SoftwareArtifactReady(ctx, sha256)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, ErrNotFound
	}
	file, err := s.Blobs.Open(sha256)
	if err != nil {
		return nil, ErrNotFound
	}
	return file, nil
}

// OpenNamedArtifact opens a ready artifact by release version and file name.
func (s *Service) OpenNamedArtifact(ctx context.Context, version, name string) (*os.File, Artifact, error) {
	artifact, err := s.Repository.SoftwareArtifact(ctx, version, name)
	if err != nil {
		return nil, Artifact{}, err
	}
	if !artifact.Ready {
		return nil, Artifact{}, ErrNotFound
	}
	file, err := s.Blobs.Open(artifact.SHA256)
	if err != nil {
		return nil, Artifact{}, ErrNotFound
	}
	return file, artifact, nil
}

// PlayerTarget is the Assignment.software value for a node and its software
// generation, which changes whenever the target is set or cleared.
func (s *Service) PlayerTarget(ctx context.Context, nodeID string) (*client.SoftwareTarget, int64, error) {
	version, generation, method, err := s.Repository.PlayerSoftwareTarget(ctx, nodeID)
	if err != nil || version == "" {
		return nil, generation, err
	}
	target := &client.SoftwareTarget{
		Version:          version,
		ManifestURL:      s.publicURL("/api/v1/player/software/releases/" + url.PathEscape(version)),
		ArtifactsBaseURL: s.publicURL("/api/v1/player/software/artifacts/"),
	}
	if method == MethodAndroid {
		release, err := s.Repository.GetSoftwareRelease(ctx, version)
		if err != nil {
			return nil, generation, err
		}
		if apk, ok := findReady(release, updates.ComponentAndroid, "android", "any", "apk"); ok {
			target.APKURL = s.publicURL(downloadPath(version, apk.Name))
			target.APKSHA256 = apk.SHA256
		}
	}
	return target, generation, nil
}

func (s *Service) publicURL(path string) string {
	return strings.TrimRight(s.PublicURL, "/") + path
}

// downloadPath is the public mirror path of a release artifact.
func downloadPath(version, name string) string {
	return "/updates/" + url.PathEscape(version) + "/" + url.PathEscape(name)
}

func findReady(release Release, component, osName, arch, format string) (Artifact, bool) {
	for _, artifact := range release.Artifacts {
		if artifact.Ready && artifact.Component == component && artifact.OS == osName && artifact.Arch == arch && artifact.Format == format {
			return artifact, true
		}
	}
	return Artifact{}, false
}

// installable reports whether release carries ready artifacts for the node's
// update method and platform.
func installable(release Release, report *client.CapabilityReport) bool {
	if report == nil {
		return false
	}
	switch report.UpdateMethod {
	case MethodDesktop:
		ymlArch := "any"
		if report.Platform == "linux" {
			ymlArch = report.Architecture
		}
		if _, ok := findReady(release, updates.ComponentDesktop, report.Platform, ymlArch, "yml"); !ok {
			return false
		}
		// The updater feed references the platform's installers; all of them
		// must be mirrored.
		for _, artifact := range release.Artifacts {
			if artifact.Component != updates.ComponentDesktop || artifact.OS != report.Platform {
				continue
			}
			if report.Platform == "linux" && artifact.Arch != report.Architecture && artifact.Arch != "any" {
				continue
			}
			if !artifact.Ready {
				return false
			}
		}
		return true
	case MethodAppliance:
		_, ok := findReady(release, updates.ComponentPlayer, report.Platform, report.Architecture, "tar.gz")
		return ok
	case MethodAndroid:
		_, ok := findReady(release, updates.ComponentAndroid, "android", "any", "apk")
		return ok
	default:
		return false
	}
}

func channelAllows(nodeChannel, releaseChannel string) bool {
	return releaseChannel == updates.ChannelStable || nodeChannel == updates.ChannelBeta
}

func compare(a, b string) (int, bool) {
	order, err := updates.CompareVersions(a, b)
	return order, err == nil
}
