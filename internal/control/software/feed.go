package software

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxEnvelopeBytes = 1 << 20

// Feed configures release feed polling.
type Feed struct {
	URL      string
	Enabled  bool
	Interval time.Duration
	// Client defaults to an http.Client without an overall timeout; requests
	// are bounded by the check context.
	Client *http.Client
}

// FeedStatus is the operator view of the feed.
type FeedStatus struct {
	Enabled         bool       `json:"enabled"`
	URL             string     `json:"url"`
	IntervalSeconds int64      `json:"intervalSeconds"`
	LastCheckedAt   *time.Time `json:"lastCheckedAt,omitempty"`
	LastSuccessAt   *time.Time `json:"lastSuccessAt,omitempty"`
	LastError       string     `json:"lastError,omitempty"`
	LastVersion     string     `json:"lastVersion,omitempty"`
}

// FeedStatus reports the configuration and last outcome of the feed.
func (s *Service) FeedStatus(ctx context.Context) (FeedStatus, error) {
	state, err := s.Repository.SoftwareFeedState(ctx)
	if err != nil {
		return FeedStatus{}, err
	}
	status := FeedStatus{Enabled: s.Feed.Enabled, URL: s.Feed.URL, IntervalSeconds: int64(s.Feed.Interval / time.Second)}
	if state.URL == s.Feed.URL {
		status.LastCheckedAt, status.LastSuccessAt, status.LastError, status.LastVersion = state.LastCheckedAt, state.LastSuccessAt, state.LastError, state.LastVersion
	}
	return status, nil
}

// RunFeed checks the feed at start and then every interval until ctx ends.
// Failures are logged and recorded; they never stop control.
func (s *Service) RunFeed(ctx context.Context) {
	if !s.Feed.Enabled || s.Feed.URL == "" {
		return
	}
	interval := s.Feed.Interval
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := s.CheckFeed(ctx); err != nil && ctx.Err() == nil {
			s.logger().Warn("software update feed check failed", "url", s.Feed.URL, "error", err)
		}
		timer.Reset(interval)
	}
}

// CheckFeed fetches the feed envelope, imports the release, and mirrors its
// artifacts from the same base URL. The outcome is recorded either way.
func (s *Service) CheckFeed(ctx context.Context) (ImportResult, error) {
	if s.Feed.URL == "" {
		return ImportResult{}, fmt.Errorf("%w: no update feed URL is configured", ErrInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()
	result, checkErr := s.checkFeed(ctx)
	state, err := s.Repository.SoftwareFeedState(ctx)
	if err != nil {
		return result, errors.Join(checkErr, err)
	}
	now := time.Now().UTC()
	state.URL = s.Feed.URL
	state.LastCheckedAt = &now
	if checkErr != nil {
		state.LastError = checkErr.Error()
	} else {
		state.LastError = ""
		state.LastSuccessAt = &now
		state.LastVersion = result.Version
	}
	if err := s.Repository.PutSoftwareFeedState(ctx, state); err != nil {
		return result, errors.Join(checkErr, err)
	}
	return result, checkErr
}

func (s *Service) checkFeed(ctx context.Context) (ImportResult, error) {
	feedURL, err := url.Parse(s.Feed.URL)
	if err != nil || (feedURL.Scheme != "https" && feedURL.Scheme != "http") || feedURL.Host == "" {
		return ImportResult{}, fmt.Errorf("%w: update feed URL %q is not an HTTP(S) URL", ErrInvalid, s.Feed.URL)
	}
	body, status, err := s.fetch(ctx, feedURL.String())
	if err != nil {
		return ImportResult{}, err
	}
	if status != http.StatusOK {
		body.Close()
		return ImportResult{}, fmt.Errorf("fetch %s: HTTP %d", feedURL, status)
	}
	document, err := io.ReadAll(io.LimitReader(body, maxEnvelopeBytes+1))
	body.Close()
	if err != nil {
		return ImportResult{}, fmt.Errorf("read %s: %w", feedURL, err)
	}
	if len(document) > maxEnvelopeBytes {
		return ImportResult{}, fmt.Errorf("%w: release envelope exceeds %d bytes", ErrInvalid, maxEnvelopeBytes)
	}
	importer, err := s.BeginImport(ctx, SourceFeed, document)
	if err != nil {
		return ImportResult{}, err
	}
	var failures []string
	for _, artifact := range importer.Artifacts() {
		if !importer.Needs(artifact.Name) {
			continue
		}
		artifactURL := feedURL.ResolveReference(&url.URL{Path: artifact.Name})
		body, status, err := s.fetch(ctx, artifactURL.String())
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if status != http.StatusOK {
			body.Close()
			if status != http.StatusNotFound {
				failures = append(failures, fmt.Sprintf("fetch %s: HTTP %d", artifact.Name, status))
			}
			continue
		}
		err = importer.PutArtifact(ctx, artifact.Name, body)
		body.Close()
		if err != nil && !errors.Is(err, ErrArtifactMismatch) {
			failures = append(failures, err.Error())
		}
	}
	result, err := importer.Finish()
	if err != nil {
		failures = append(failures, err.Error())
	}
	if len(failures) > 0 {
		return result, errors.New(strings.Join(failures, "; "))
	}
	return result, nil
}

func (s *Service) fetch(ctx context.Context, target string) (io.ReadCloser, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("User-Agent", "cuelight-control/"+s.ControlVersion)
	client := s.Feed.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	return response.Body, response.StatusCode, nil
}
