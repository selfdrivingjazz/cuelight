// Package screenshot executes durable screenshot jobs on a player node.
package screenshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image/png"
	"io"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/control/preview"
	"github.com/selfdrivingjazz/cuelight/internal/media/localcas"
)

var (
	ErrUnsupported     = errors.New("screenshot renderer is unsupported")
	ErrCaptureTooLarge = errors.New("screenshot exceeds the configured byte limit")
	ErrInvalidPNG      = errors.New("renderer did not return a valid PNG image")
)

const DefaultMaxBytes int64 = 16 << 20

type Jobs interface {
	Next(context.Context) (preview.ScreenshotJob, bool, error)
	Claim(context.Context, string) (preview.ScreenshotJob, error)
	CompletePNG(context.Context, string, string, string, int64, io.Reader) (preview.ScreenshotJob, error)
	Complete(context.Context, string, preview.Completion) (preview.ScreenshotJob, error)
}

type Capturer interface {
	Capture(context.Context) ([]byte, error)
}

type Cache interface {
	Put(io.Reader, string) (localcas.Object, error)
	Open(string) (io.ReadCloser, error)
}

type Config struct {
	NodeID   string
	MaxBytes int64
}
type Executor struct {
	Jobs     Jobs
	Renderer Capturer
	Cache    Cache
	NodeID   string
	MaxBytes int64
}

func New(config Config, jobs Jobs, renderer Capturer, cache Cache) (*Executor, error) {
	if jobs == nil || renderer == nil || cache == nil {
		return nil, errors.New("screenshot jobs, renderer, and cache are required")
	}
	if strings.TrimSpace(config.NodeID) == "" {
		return nil, errors.New("screenshot node ID is required")
	}
	limit := config.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	return &Executor{Jobs: jobs, Renderer: renderer, Cache: cache, NodeID: config.NodeID, MaxBytes: limit}, nil
}

func (e *Executor) RunOnce(ctx context.Context) (bool, error) {
	if e == nil || e.Jobs == nil || e.Renderer == nil || e.Cache == nil {
		return false, errors.New("screenshot executor is not configured")
	}
	job, ok, err := e.Jobs.Next(ctx)
	if err != nil || !ok {
		return ok, err
	}
	if job.NodeID != e.NodeID || job.Status != preview.StatusQueued {
		return true, fmt.Errorf("%w: discovered job is not queued for this node", preview.ErrForbidden)
	}
	claimed, err := e.Jobs.Claim(ctx, job.ID)
	if err != nil {
		return true, err
	}
	if claimed.NodeID != e.NodeID || claimed.Status != preview.StatusClaimed {
		return true, fmt.Errorf("%w: claim returned an invalid job", preview.ErrConflict)
	}

	image, err := e.Renderer.Capture(ctx)
	if err != nil {
		status := preview.StatusFailed
		if errors.Is(err, ErrUnsupported) {
			status = preview.StatusUnsupported
		}
		_, completeErr := e.Jobs.Complete(ctx, claimed.ID, preview.Completion{Status: status, ErrorReason: err.Error()})
		if completeErr != nil {
			return true, errors.Join(err, completeErr)
		}
		return true, err
	}
	var captureErr error
	if len(image) == 0 {
		captureErr = ErrInvalidPNG
	} else if int64(len(image)) > e.MaxBytes {
		captureErr = ErrCaptureTooLarge
	} else if _, decodeErr := png.DecodeConfig(bytes.NewReader(image)); decodeErr != nil {
		captureErr = fmt.Errorf("%w: %v", ErrInvalidPNG, decodeErr)
	}
	if captureErr != nil {
		_, completeErr := e.Jobs.Complete(ctx, claimed.ID, preview.Completion{Status: preview.StatusFailed, ErrorReason: captureErr.Error()})
		if completeErr != nil {
			return true, errors.Join(captureErr, completeErr)
		}
		return true, captureErr
	}
	digestBytes := sha256.Sum256(image)
	digest := hex.EncodeToString(digestBytes[:])
	object, err := e.Cache.Put(bytes.NewReader(image), digest)
	if err != nil {
		return true, fmt.Errorf("cache screenshot: %w", err)
	}
	body, err := e.Cache.Open(object.Digest)
	if err != nil {
		return true, fmt.Errorf("open cached screenshot: %w", err)
	}
	defer body.Close()
	if object.Digest != digest || object.Size != int64(len(image)) {
		return true, fmt.Errorf("%w: cached screenshot metadata mismatch", ErrInvalidPNG)
	}
	_, err = e.Jobs.CompletePNG(ctx, claimed.ID, digest, "image/png", object.Size, body)
	return true, err
}
