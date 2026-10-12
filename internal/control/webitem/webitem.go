// Package webitem keeps viewport thumbnails for web media. Thumbnails come
// from a server-side headless browser capture and from node screenshots taken
// while a node is showing the page, whichever is newest.
package webitem

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/control/media"
)

const (
	SourceServer = "server"
	SourceNode   = "node"
	maxTitle     = 255
)

// Item is the capture view of one web media row with a URL.
type Item struct {
	ID  string
	URL string
}

type Repository interface {
	// WebCaptureTarget returns the web media item; Hosted items (no URL) and
	// file media report media.ErrNotFound.
	WebCaptureTarget(ctx context.Context, id string) (Item, error)
	// SetWebMetadata records the favicon and, when the title is still the
	// default URL, the rendered page title.
	SetWebMetadata(ctx context.Context, id, title, faviconURL string) error
	// StoreWebThumbnail registers the PNG bytes as an artifact and makes it the
	// item's thumbnail unless a newer one is already recorded.
	StoreWebThumbnail(ctx context.Context, id string, png []byte, source string, capturedAt time.Time) error
	RecordWebCaptureFailure(ctx context.Context, id, reason string, at time.Time) error
	// AdoptNodeScreenshots makes the newest completed screenshot of a node whose
	// current assignment targets a web media item that item's thumbnail.
	AdoptNodeScreenshots(ctx context.Context) (int, error)
	// WebDueForCapture returns items whose server capture is missing or older than before.
	WebDueForCapture(ctx context.Context, before time.Time, limit int) ([]Item, error)
}

// Capture is a rendered viewport and the page metadata observed while rendering.
type Capture struct {
	PNG        []byte
	Title      string
	FaviconURL string
}

type Capturer interface {
	Capture(ctx context.Context, url string) (Capture, error)
}

// Service implements media.WebCapture.
type Service struct {
	Repository Repository
	Capturer   Capturer // nil disables server-side capture
	Now        func() time.Time
	Logger     *slog.Logger
	// RefreshAfter is how old a server capture may get before it is redone.
	RefreshAfter time.Duration

	inflight sync.Map // media id -> struct{}
}

var _ media.WebCapture = (*Service)(nil)

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// Capture renders the page in the background.
func (s *Service) Capture(id, url string) {
	if s.Capturer == nil || url == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		item, err := s.Repository.WebCaptureTarget(ctx, id)
		if err == nil {
			err = s.capture(ctx, item)
		}
		if err != nil {
			s.logger().Warn("web media capture failed", "id", id, "url", url, "error", err)
		}
	}()
}

// Refresh captures the page now. Capture failures are recorded on the item
// rather than returned, so callers re-read the item to see the outcome.
func (s *Service) Refresh(ctx context.Context, id string) error {
	if s.Capturer == nil {
		return media.ErrCaptureDisabled
	}
	item, err := s.Repository.WebCaptureTarget(ctx, id)
	if err != nil {
		return err
	}
	if err := s.capture(ctx, item); err != nil {
		s.logger().Warn("web media capture failed", "id", item.ID, "url", item.URL, "error", err)
	}
	return nil
}

// capture renders one item; concurrent captures of the same item collapse.
func (s *Service) capture(ctx context.Context, item Item) error {
	if _, busy := s.inflight.LoadOrStore(item.ID, struct{}{}); busy {
		return nil
	}
	defer s.inflight.Delete(item.ID)
	result, err := s.Capturer.Capture(ctx, item.URL)
	now := s.now()
	if err != nil {
		if recordErr := s.Repository.RecordWebCaptureFailure(ctx, item.ID, err.Error(), now); recordErr != nil {
			return errors.Join(err, recordErr)
		}
		return err
	}
	if err := s.Repository.SetWebMetadata(ctx, item.ID, truncate(strings.TrimSpace(result.Title), maxTitle), result.FaviconURL); err != nil {
		return err
	}
	return s.Repository.StoreWebThumbnail(ctx, item.ID, result.PNG, SourceServer, now)
}

// Run keeps web thumbnails fresh until ctx ends: adopts node screenshots and
// refreshes stale server captures.
func (s *Service) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Sync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sync performs one maintenance pass.
func (s *Service) Sync(ctx context.Context) {
	if _, err := s.Repository.AdoptNodeScreenshots(ctx); err != nil {
		s.logger().Warn("adopt node screenshots for web media", "error", err)
	}
	if s.Capturer == nil {
		return
	}
	refreshAfter := s.RefreshAfter
	if refreshAfter <= 0 {
		refreshAfter = 6 * time.Hour
	}
	due, err := s.Repository.WebDueForCapture(ctx, s.now().Add(-refreshAfter), 4)
	if err != nil {
		s.logger().Warn("list web media due for capture", "error", err)
		return
	}
	for _, item := range due {
		captureCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		if err := s.capture(captureCtx, item); err != nil {
			s.logger().Warn("web media capture failed", "id", item.ID, "url", item.URL, "error", err)
		}
		cancel()
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	for len(string(runes)) > limit {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}
