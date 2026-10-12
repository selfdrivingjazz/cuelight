package media

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/media/localcas"
)

type RegistrationService struct {
	repository AssetRepository
	cas        CAS
	probe      CertifiedProbe
	opener     SafeOpener
	copier     SourceCopier
	newID      IDGenerator
	policy     LocalRegistrationPolicy
	roots      []string
}

func NewRegistrationService(repository AssetRepository, cas CAS, probe CertifiedProbe, opener SafeOpener, copier SourceCopier, newID IDGenerator, policy LocalRegistrationPolicy) (*RegistrationService, error) {
	if repository == nil || cas == nil || probe == nil || opener == nil || copier == nil || newID == nil {
		return nil, &ValidationError{Field: "dependencies", Reason: "repository, CAS, probe, opener, copier, and ID generator are required"}
	}
	if policy.MaxBytes <= 0 {
		return nil, &ValidationError{Field: "maxBytes", Reason: "must be positive"}
	}
	roots := make([]string, 0, len(policy.AllowlistedRoots))
	for _, configured := range policy.AllowlistedRoots {
		if strings.TrimSpace(configured) == "" {
			return nil, &ValidationError{Field: "allowlistedRoots", Reason: "roots cannot be empty"}
		}
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return nil, fmt.Errorf("resolve allowlisted root: %w", err)
		}
		roots = append(roots, filepath.Clean(absolute))
	}
	sort.Slice(roots, func(left, right int) bool { return len(roots[left]) > len(roots[right]) })
	if policy.Enabled && len(roots) == 0 {
		return nil, &ValidationError{Field: "allowlistedRoots", Reason: "at least one root is required when local registration is enabled"}
	}
	return &RegistrationService{
		repository: repository,
		cas:        cas,
		probe:      probe,
		opener:     opener,
		copier:     copier,
		newID:      newID,
		policy:     policy,
		roots:      roots,
	}, nil
}

func (s *RegistrationService) Register(ctx context.Context, mutation Mutation, path string) (Asset, error) {
	if err := validateMutation(mutation, true); err != nil {
		return Asset{}, err
	}
	if !s.policy.Enabled {
		return Asset{}, ErrRegistrationDisabled
	}
	if strings.TrimSpace(path) == "" {
		return Asset{}, &ValidationError{Field: "path", Reason: "is required"}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Asset{}, fmt.Errorf("resolve local media path: %w", err)
	}
	absolute = filepath.Clean(absolute)
	root, relative, ok := allowedRelativePath(s.roots, absolute)
	if !ok {
		return Asset{}, ErrPathNotAllowed
	}

	source, err := s.opener.Open(root, relative)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			return Asset{}, err
		}
		return Asset{}, fmt.Errorf("%w: %v", ErrUnsafeSource, err)
	}
	defer source.Close()
	before, err := source.Version()
	if err != nil {
		return Asset{}, fmt.Errorf("inspect local media source: %w", err)
	}
	if !before.regular() {
		return Asset{}, ErrUnsafeSource
	}
	if before.Size <= 0 {
		return Asset{}, &ValidationError{Field: "path", Reason: "source file must not be empty"}
	}
	if before.Size > s.policy.MaxBytes {
		return Asset{}, &LimitError{Kind: ErrUploadTooLarge, Limit: s.policy.MaxBytes, Value: before.Size}
	}

	object, err := s.copier.Copy(ctx, s.cas, source, s.policy.MaxBytes)
	if err != nil {
		return Asset{}, err
	}
	after, err := source.Version()
	if err != nil {
		return Asset{}, fmt.Errorf("reinspect local media source: %w", err)
	}
	if before != after || object.Size != before.Size {
		return Asset{}, ErrSourceChanged
	}
	if err := localcas.ValidateDigest(object.Digest); err != nil {
		return Asset{}, fmt.Errorf("CAS returned invalid digest: %w", err)
	}
	certification, err := s.probe.Probe(ctx, object)
	if err != nil {
		return Asset{}, err
	}
	if !certification.Certified || strings.TrimSpace(certification.MediaType) == "" {
		return Asset{}, ErrMediaUncertified
	}
	assetID := s.newID()
	if strings.TrimSpace(assetID) == "" {
		return Asset{}, errors.New("media ID generator returned an empty asset ID")
	}
	artifact := Artifact{Digest: object.Digest, Size: object.Size, MediaType: certification.MediaType, State: ArtifactReady}
	asset := Asset{ID: assetID, Name: filepath.Base(absolute), Artifact: artifact}
	return s.repository.RegisterAsset(ctx, mutation, asset)
}

func allowedRelativePath(roots []string, absolute string) (string, string, bool) {
	for _, root := range roots {
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		return root, relative, true
	}
	return "", "", false
}
