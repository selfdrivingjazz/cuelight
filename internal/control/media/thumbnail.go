package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"strings"
	"time"

	// Registered decoders for formats the controller can thumbnail itself.
	_ "image/gif"
	_ "image/png"
)

const (
	// ThumbnailMaxEdge bounds the longest side of a persisted poster.
	ThumbnailMaxEdge = 640
	// ThumbnailMaxSourcePixels rejects decompression bombs before decoding.
	ThumbnailMaxSourcePixels = 64 << 20
	// ThumbnailMaxUploadBytes bounds operator-captured poster uploads.
	ThumbnailMaxUploadBytes = 8 << 20
	ThumbnailMediaType      = "image/jpeg"
)

// ErrThumbnailUnsupported reports a source the controller cannot decode.
var ErrThumbnailUnsupported = errors.New("thumbnail source format is not supported")

type Thumbnail struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	// Source ("server" or "node") and CapturedAt describe web captures.
	Source     string     `json:"source,omitempty"`
	CapturedAt *time.Time `json:"capturedAt,omitempty"`
}

type ThumbnailRepository interface {
	// PutMediaThumbnail registers the stored JPEG artifact and links it to the
	// source artifact digest, replacing any previous thumbnail.
	PutMediaThumbnail(ctx context.Context, sourceDigest string, thumbnail Thumbnail, size int64) error
	// ListThumbnailSources returns ready image artifacts referenced by media
	// that have no thumbnail yet.
	ListThumbnailSources(ctx context.Context, limit int) ([]Artifact, error)
}

// ThumbnailService generates, persists, and serves content-addressed posters.
type ThumbnailService struct {
	Repository ThumbnailRepository
	CAS        CAS
	Content    ContentReader
}

// EncodeThumbnail decodes a PNG, JPEG, or GIF, scales it to fit within
// ThumbnailMaxEdge, and re-encodes it as JPEG.
func EncodeThumbnail(source []byte) ([]byte, int, int, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(source))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%w: %v", ErrThumbnailUnsupported, err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > ThumbnailMaxSourcePixels {
		return nil, 0, 0, &ValidationError{Field: "thumbnail", Reason: "image dimensions are out of range"}
	}
	decoded, _, err := image.Decode(bytes.NewReader(source))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%w: %v", ErrThumbnailUnsupported, err)
	}
	scaled := downscale(decoded, ThumbnailMaxEdge)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, scaled, &jpeg.Options{Quality: 82}); err != nil {
		return nil, 0, 0, fmt.Errorf("encode thumbnail: %w", err)
	}
	bounds := scaled.Bounds()
	return out.Bytes(), bounds.Dx(), bounds.Dy(), nil
}

// downscale box-filters src so its longest edge is at most maxEdge. Alpha is
// composited onto black, matching how displays letterbox media.
func downscale(src image.Image, maxEdge int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := sw, sh
	if sw > maxEdge || sh > maxEdge {
		if sw >= sh {
			dw, dh = maxEdge, max(1, sh*maxEdge/sw)
		} else {
			dw, dh = max(1, sw*maxEdge/sh), maxEdge
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		y0, y1 := b.Min.Y+y*sh/dh, b.Min.Y+max((y+1)*sh/dh, y*sh/dh+1)
		for x := range dw {
			x0, x1 := b.Min.X+x*sw/dw, b.Min.X+max((x+1)*sw/dw, x*sw/dw+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 0xff})
		}
	}
	return dst
}

// Store encodes source as a thumbnail for sourceDigest and persists it.
func (s *ThumbnailService) Store(ctx context.Context, sourceDigest string, source []byte) (Thumbnail, error) {
	if s == nil || s.Repository == nil || s.CAS == nil {
		return Thumbnail{}, errors.New("thumbnail service is not configured")
	}
	encoded, width, height, err := EncodeThumbnail(source)
	if err != nil {
		return Thumbnail{}, err
	}
	if err := ctx.Err(); err != nil {
		return Thumbnail{}, err
	}
	object, err := s.CAS.Put(bytes.NewReader(encoded), "")
	if err != nil {
		return Thumbnail{}, fmt.Errorf("store thumbnail: %w", err)
	}
	thumbnail := Thumbnail{Digest: object.Digest, MediaType: ThumbnailMediaType, Width: width, Height: height}
	if err := s.Repository.PutMediaThumbnail(ctx, sourceDigest, thumbnail, object.Size); err != nil {
		return Thumbnail{}, err
	}
	return thumbnail, nil
}

// Generate thumbnails an artifact the controller can decode itself. Other
// formats (video, WebP, audio) return ErrThumbnailUnsupported; the operator
// UI captures and uploads a poster frame for those.
func (s *ThumbnailService) Generate(ctx context.Context, artifact Artifact) (Thumbnail, error) {
	if !strings.HasPrefix(artifact.MediaType, "image/") || artifact.State != ArtifactReady {
		return Thumbnail{}, ErrThumbnailUnsupported
	}
	if s.Content == nil {
		return Thumbnail{}, errors.New("thumbnail content reader is not configured")
	}
	handle, err := s.Content.Open(ctx, artifact.Digest)
	if err != nil {
		return Thumbnail{}, err
	}
	defer handle.Close()
	source, err := io.ReadAll(io.LimitReader(handle, artifact.Size+1))
	if err != nil {
		return Thumbnail{}, fmt.Errorf("read thumbnail source: %w", err)
	}
	return s.Store(ctx, artifact.Digest, source)
}

// Backfill thumbnails every decodable image that lacks one. Undecodable
// sources are logged and skipped; they are retried on the next startup.
func (s *ThumbnailService) Backfill(ctx context.Context, logger *slog.Logger) {
	sources, err := s.Repository.ListThumbnailSources(ctx, 1000)
	if err != nil {
		logger.Warn("list thumbnail sources", "error", err)
		return
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		if _, err := s.Generate(ctx, source); err != nil {
			logger.Info("skip media thumbnail", "digest", source.Digest, "mediaType", source.MediaType, "error", err)
		}
	}
}
