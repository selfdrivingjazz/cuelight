package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	"github.com/jmilldotdev/cuelight/internal/player/screenshot"
	"github.com/jmilldotdev/cuelight/internal/release"
)

type RemoteDescriptor struct {
	SessionID string
	// Generation is the desired generation the session was created for; a
	// renewal creates the replacement session for the same generation.
	Generation int64
	URL        string
	ShowQR     bool
	ExpiresAt  time.Time
}

type ScheduleItem struct {
	ID                  string
	AssetID             string
	MediaID             string
	StartsAt            time.Time
	EndsAt              time.Time
	Source              string
	MediaType           string
	Fit                 string
	Loop                bool
	Muted               bool
	Volume              int
	ImageDurationSecond int
	Orientation         int
	// WebURL is set for web schedule items, which render a live page for the
	// item's duration instead of a media Source.
	WebURL string
}

type Candidate struct {
	Kind                release.PresentationKind
	ReleaseDigest       string
	MediaID             string
	AssetID             string
	Source              string
	MediaType           string
	Fit                 string
	Loop                bool
	Muted               bool
	Volume              int
	ImageDurationSecond int
	Orientation         int
	Timezone            string
	WindowStart         time.Time
	WindowEnd           time.Time
	Schedule            []ScheduleItem
	// WebURL is the live website shown by the Chromium adapter for web
	// presentations. AppAccess pages (MediaID names their media item) are
	// opened with an app token and consume remote input themselves.
	WebURL    string
	AppAccess bool
	Remote    *RemoteDescriptor
}

func sameCandidate(a, b Candidate) bool { return reflect.DeepEqual(a, b) }

type Status struct {
	ReleaseDigest string
	Healthy       bool
	Path          string
	Candidate     Candidate
	Current       *playerclient.CurrentContent
}

type Adapter interface {
	Prepare(context.Context, Candidate) error
	Start(context.Context, Candidate) error
	Health(context.Context, Candidate) (Status, error)
	Commit(context.Context, Candidate) error
	Stop(context.Context) error
	Current(context.Context) (Status, error)
}

type MPV struct {
	SocketPath     string
	Timeout        time.Duration
	Now            func() time.Time
	requestID      atomic.Uint64
	current        atomic.Pointer[Candidate]
	scheduleMu     sync.Mutex
	scheduleStop   context.CancelFunc
	scheduleErr    error
	scheduleItem   string
	startedAt      time.Time
	transitionedAt time.Time
}
type mpvResponse struct {
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
	RequestID uint64          `json:"request_id"`
}

func (m *MPV) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// SelectScheduleItem applies half-open [startsAt, endsAt) boundaries.
func SelectScheduleItem(items []ScheduleItem, now time.Time) (ScheduleItem, bool) {
	for _, item := range items {
		if !now.Before(item.StartsAt) && now.Before(item.EndsAt) {
			return item, true
		}
	}
	return ScheduleItem{}, false
}

func (m *MPV) Prepare(_ context.Context, candidate Candidate) error {
	if candidate.ReleaseDigest == "" {
		return errors.New("release digest is required")
	}
	if candidate.Kind != "" && candidate.Kind != release.PresentationMedia && candidate.Kind != release.PresentationMediaSchedule {
		return errors.New("MPV supports only media and media-schedule presentations")
	}
	if candidate.Kind == release.PresentationMediaSchedule {
		if candidate.Source != "" {
			return errors.New("schedule candidate must not set a singular source")
		}
		if len(candidate.Schedule) == 0 || !candidate.WindowStart.Before(candidate.WindowEnd) {
			return errors.New("schedule window and items are required")
		}
		for _, item := range candidate.Schedule {
			if item.Source == "" || !allowedMPVSource(item.Source) || !item.StartsAt.Before(item.EndsAt) {
				return errors.New("schedule item source or window is invalid")
			}
			if err := validatePlaybackValues(item.Fit, item.Volume, item.ImageDurationSecond, item.Orientation); err != nil {
				return err
			}
		}
		return nil
	}
	if candidate.Source == "" || !allowedMPVSource(candidate.Source) {
		return errors.New("release digest and local or loopback source are required")
	}
	return validatePlaybackValues(candidate.Fit, candidate.Volume, candidate.ImageDurationSecond, candidate.Orientation)
}

func validatePlaybackValues(fit string, volume, imageDuration, orientation int) error {
	if fit != "fit" && fit != "fill" {
		return errors.New("fit must be fit or fill")
	}
	if volume < 0 || volume > 100 || imageDuration < 0 {
		return errors.New("volume and image duration are invalid")
	}
	switch orientation {
	case 0, 90, 180, 270:
	default:
		return errors.New("orientation must be 0, 90, 180, or 270")
	}
	return nil
}

func allowedMPVSource(source string) bool {
	if strings.HasPrefix(source, "/") {
		return true
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Host == "" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (m *MPV) Start(ctx context.Context, candidate Candidate) error {
	if err := m.Prepare(ctx, candidate); err != nil {
		return err
	}
	m.scheduleMu.Lock()
	if m.scheduleStop != nil {
		m.scheduleStop()
	}
	m.scheduleStop, m.scheduleErr, m.scheduleItem = nil, nil, ""
	m.scheduleMu.Unlock()
	if candidate.Kind == release.PresentationMediaSchedule {
		now := m.now()
		item, active := SelectScheduleItem(candidate.Schedule, now)
		if active {
			if err := m.playItem(ctx, item); err != nil {
				return err
			}
			m.setScheduleState(item.ID, nil)
		} else if now.Before(candidate.WindowStart) {
			if _, err := m.command(ctx, []any{"stop"}); err != nil {
				return fmt.Errorf("clear MPV before schedule window: %w", err)
			}
		} else if now.Before(candidate.WindowEnd) {
			return errors.New("media schedule has no active item inside its window")
		}
		scheduleCtx, cancel := context.WithCancel(context.Background())
		m.scheduleMu.Lock()
		m.scheduleStop, m.scheduleErr, m.scheduleItem = cancel, nil, ""
		m.startedAt, m.transitionedAt = now.UTC(), now.UTC()
		if active {
			m.scheduleItem, m.transitionedAt = item.ID, now.UTC()
		}
		m.scheduleMu.Unlock()
		go m.runSchedule(scheduleCtx, candidate)
	} else if err := m.playItem(ctx, ScheduleItem{AssetID: candidate.AssetID, MediaID: candidate.MediaID, Source: candidate.Source, Fit: candidate.Fit, Loop: candidate.Loop, Muted: candidate.Muted, Volume: candidate.Volume, ImageDurationSecond: candidate.ImageDurationSecond, Orientation: candidate.Orientation}); err != nil {
		return err
	}
	copy := candidate
	m.current.Store(&copy)
	if candidate.Kind != release.PresentationMediaSchedule {
		m.scheduleMu.Lock()
		m.startedAt, m.transitionedAt = m.now().UTC(), m.now().UTC()
		m.scheduleMu.Unlock()
	}
	return nil
}

func (m *MPV) playItem(ctx context.Context, item ScheduleItem) error {
	if _, err := m.command(ctx, []any{"loadfile", item.Source, "replace"}); err != nil {
		return err
	}
	for _, property := range itemProperties(item) {
		if _, err := m.command(ctx, []any{"set_property", property.name, property.value}); err != nil {
			return fmt.Errorf("set mpv property %s: %w", property.name, err)
		}
	}
	return nil
}

func (m *MPV) runSchedule(ctx context.Context, candidate Candidate) {
	for {
		now := m.now()
		if now.Before(candidate.WindowStart) {
			if !waitFor(ctx, candidate.WindowStart.Sub(now)) {
				return
			}
			continue
		}
		if !now.Before(candidate.WindowEnd) {
			_, err := m.command(ctx, []any{"stop"})
			m.setScheduleState("", err)
			return
		}
		item, ok := SelectScheduleItem(candidate.Schedule, now)
		if !ok {
			m.setScheduleState("", errors.New("media schedule has no active item inside its window"))
			return
		}
		currentItem, _ := m.scheduleState()
		if currentItem != item.ID {
			if err := m.playItem(ctx, item); err != nil {
				m.setScheduleState("", err)
				retry := min(time.Second, item.EndsAt.Sub(now))
				if retry <= 0 || !waitFor(ctx, retry) {
					return
				}
				continue
			}
			m.setScheduleState(item.ID, nil)
		}
		if !waitFor(ctx, item.EndsAt.Sub(now)) {
			return
		}
	}
}

func waitFor(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return true
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (m *MPV) setScheduleState(itemID string, err error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	if itemID != "" && itemID != m.scheduleItem {
		m.transitionedAt = m.now().UTC()
	}
	m.scheduleItem, m.scheduleErr = itemID, err
}

func (m *MPV) scheduleState() (string, error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	return m.scheduleItem, m.scheduleErr
}

func (m *MPV) Health(ctx context.Context, candidate Candidate) (Status, error) {
	now := m.now().UTC()
	if candidate.Kind == release.PresentationMediaSchedule {
		if now.Before(candidate.WindowStart) || !now.Before(candidate.WindowEnd) {
			_, scheduleErr := m.scheduleState()
			healthy := scheduleErr == nil
			return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: healthy, Candidate: candidate, Current: m.currentContent(candidate, healthy, "", now)}, scheduleErr
		}
		if _, scheduleErr := m.scheduleState(); scheduleErr != nil {
			return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: false, Candidate: candidate, Current: m.currentContent(candidate, false, "", now)}, scheduleErr
		}
	}
	response, err := m.command(ctx, []any{"get_property", "path"})
	if err != nil {
		return Status{}, err
	}
	var path string
	if err := json.Unmarshal(response.Data, &path); err != nil {
		return Status{}, fmt.Errorf("decode mpv path: %w", err)
	}
	expected := ScheduleItem{AssetID: candidate.AssetID, MediaID: candidate.MediaID, Source: candidate.Source, Fit: candidate.Fit, Loop: candidate.Loop, Muted: candidate.Muted, Volume: candidate.Volume, ImageDurationSecond: candidate.ImageDurationSecond, Orientation: candidate.Orientation}
	if candidate.Kind == release.PresentationMediaSchedule {
		item, ok := SelectScheduleItem(candidate.Schedule, now)
		if !ok {
			return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: false, Path: path, Candidate: candidate, Current: m.currentContent(candidate, false, path, now)}, nil
		}
		expected = item
	}
	healthy := path == expected.Source
	for _, property := range itemProperties(expected) {
		response, err := m.command(ctx, []any{"get_property", property.name})
		if err != nil {
			return Status{}, err
		}
		if !sameJSONValue(response.Data, property.value) {
			healthy = false
		}
	}
	var paused bool
	if response, err := m.command(ctx, []any{"get_property", "pause"}); err == nil && len(response.Data) > 0 {
		_ = json.Unmarshal(response.Data, &paused)
	}
	return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: healthy, Path: path, Candidate: candidate, Current: m.currentContent(candidate, healthy, path, now, paused)}, nil
}

func (m *MPV) currentContent(candidate Candidate, healthy bool, path string, now time.Time, paused ...bool) *playerclient.CurrentContent {
	content := &playerclient.CurrentContent{ReleaseDigest: candidate.ReleaseDigest, PresentationKind: candidate.Kind, State: playerclient.ContentStatePlaying, TransitionedAt: now}
	if candidate.Kind == release.PresentationMediaSchedule {
		itemID, _ := m.scheduleState()
		var item *ScheduleItem
		for index := range candidate.Schedule {
			if candidate.Schedule[index].ID == itemID {
				item = &candidate.Schedule[index]
				break
			}
		}
		if item == nil {
			content.State = playerclient.ContentStateOffAir
			if !healthy {
				content.State = playerclient.ContentStateStale
			}
			return content
		}
		content.ItemID, content.AssetID, content.MediaID = item.ID, item.AssetID, item.MediaID
		start := item.StartsAt.UTC()
		content.StartedAt = &start
		m.scheduleMu.Lock()
		if !m.transitionedAt.IsZero() {
			content.TransitionedAt = m.transitionedAt
		}
		m.scheduleMu.Unlock()
	} else {
		content.AssetID, content.MediaID = candidate.AssetID, candidate.MediaID
		m.scheduleMu.Lock()
		started, transitioned := m.startedAt, m.transitionedAt
		m.scheduleMu.Unlock()
		if !started.IsZero() {
			started = started.UTC()
			content.StartedAt = &started
		}
		if !transitioned.IsZero() {
			content.TransitionedAt = transitioned.UTC()
		}
	}
	if !healthy {
		content.State = playerclient.ContentStateStale
	} else if len(paused) > 0 && paused[0] {
		content.State = playerclient.ContentStatePaused
	}
	_ = path
	return content
}

func (m *MPV) Commit(_ context.Context, candidate Candidate) error {
	current := m.current.Load()
	if current == nil || !sameCandidate(*current, candidate) {
		return errors.New("candidate is not the current renderer state")
	}
	return nil
}

func (m *MPV) Stop(ctx context.Context) error {
	m.scheduleMu.Lock()
	if m.scheduleStop != nil {
		m.scheduleStop()
		m.scheduleStop = nil
	}
	m.scheduleErr, m.scheduleItem = nil, ""
	m.scheduleMu.Unlock()
	if m.current.Load() == nil {
		return nil
	}
	if _, err := m.command(ctx, []any{"stop"}); err != nil {
		return err
	}
	m.current.Store(nil)
	return nil
}

func (m *MPV) Current(ctx context.Context) (Status, error) {
	current := m.current.Load()
	if current == nil {
		return Status{}, nil
	}
	return m.Health(ctx, *current)
}

// Capture asks MPV to encode the currently presented frame as PNG. The
// screenshot file is temporary and is removed after its bytes are read.
func (m *MPV) Capture(ctx context.Context) ([]byte, error) {
	if m.current.Load() == nil {
		return nil, screenshot.ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(os.TempDir(), fmt.Sprintf("cuelight-screenshot-%d.png", m.requestID.Add(1)))
	defer os.Remove(path)
	if _, err := m.command(ctx, []any{"screenshot-to-file", path, "video"}); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open MPV screenshot: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, screenshot.DefaultMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > screenshot.DefaultMaxBytes {
		return nil, screenshot.ErrCaptureTooLarge
	}
	return data, nil
}

type candidateProperty struct {
	name  string
	value any
}

func candidateProperties(candidate Candidate) []candidateProperty {
	return itemProperties(ScheduleItem{
		Fit: candidate.Fit, Loop: candidate.Loop, Muted: candidate.Muted,
		Volume: candidate.Volume, ImageDurationSecond: candidate.ImageDurationSecond,
		Orientation: candidate.Orientation,
	})
}

func itemProperties(item ScheduleItem) []candidateProperty {
	panscan := 0
	if item.Fit == "fill" {
		panscan = 1
	}
	loop := "no"
	if item.Loop {
		loop = "inf"
	}
	return []candidateProperty{
		{name: "panscan", value: panscan},
		{name: "loop-file", value: loop},
		{name: "mute", value: item.Muted},
		{name: "volume", value: item.Volume},
		{name: "image-display-duration", value: item.ImageDurationSecond},
		{name: "video-rotate", value: item.Orientation},
	}
}

func sameJSONValue(encoded json.RawMessage, expected any) bool {
	var actual any
	if json.Unmarshal(encoded, &actual) != nil {
		return false
	}
	normalized, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	var normalizedExpected any
	if json.Unmarshal(normalized, &normalizedExpected) != nil {
		return false
	}
	return fmt.Sprint(actual) == fmt.Sprint(normalizedExpected)
}

func (m *MPV) command(ctx context.Context, command []any) (mpvResponse, error) {
	if m.SocketPath == "" {
		return mpvResponse{}, errors.New("mpv socket path is required")
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "unix", m.SocketPath)
	if err != nil {
		return mpvResponse{}, fmt.Errorf("connect mpv IPC: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return mpvResponse{}, err
	}
	requestID := m.requestID.Add(1)
	request := struct {
		Command   []any  `json:"command"`
		RequestID uint64 `json:"request_id"`
	}{Command: command, RequestID: requestID}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return mpvResponse{}, fmt.Errorf("write mpv IPC: %w", err)
	}
	var response mpvResponse
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		return mpvResponse{}, fmt.Errorf("read mpv IPC: %w", err)
	}
	if response.RequestID != requestID {
		return mpvResponse{}, errors.New("mpv response request ID mismatch")
	}
	if response.Error != "success" {
		return mpvResponse{}, fmt.Errorf("mpv command failed: %s", response.Error)
	}
	return response, nil
}
