package renderer

import (
	"context"
	"errors"

	"github.com/selfdrivingjazz/cuelight/internal/player/screenshot"
	"github.com/selfdrivingjazz/cuelight/internal/release"
)

// Composite dispatches a materialized candidate to the adapter that owns its
// presentation kind. Empty kind is treated as media for legacy in-memory
// candidates; signed releases always carry an explicit discriminator.
type Composite struct {
	Media Adapter
	Web   Adapter
}

func NewComposite(media, web Adapter) (*Composite, error) {
	if media == nil || web == nil {
		return nil, errors.New("media and web renderers are required")
	}
	return &Composite{Media: media, Web: web}, nil
}

func (c *Composite) adapter(candidate Candidate) (Adapter, error) {
	switch candidate.Kind {
	case "", release.PresentationMedia, release.PresentationMediaSchedule:
		return c.Media, nil
	case release.PresentationWeb:
		return c.Web, nil
	default:
		return nil, errors.New("unsupported presentation kind")
	}
}

func (c *Composite) Prepare(ctx context.Context, candidate Candidate) error {
	adapter, err := c.adapter(candidate)
	if err != nil {
		return err
	}
	return adapter.Prepare(ctx, candidate)
}
func (c *Composite) Start(ctx context.Context, candidate Candidate) error {
	adapter, err := c.adapter(candidate)
	if err != nil {
		return err
	}
	return adapter.Start(ctx, candidate)
}
func (c *Composite) Health(ctx context.Context, candidate Candidate) (Status, error) {
	adapter, err := c.adapter(candidate)
	if err != nil {
		return Status{}, err
	}
	return adapter.Health(ctx, candidate)
}
func (c *Composite) Commit(ctx context.Context, candidate Candidate) error {
	adapter, err := c.adapter(candidate)
	if err != nil {
		return err
	}
	return adapter.Commit(ctx, candidate)
}
func (c *Composite) Stop(ctx context.Context) error {
	var result error
	if c.Media != nil {
		result = errors.Join(result, c.Media.Stop(ctx))
	}
	if c.Web != nil && c.Web != c.Media {
		result = errors.Join(result, c.Web.Stop(ctx))
	}
	return result
}
func (c *Composite) Current(ctx context.Context) (Status, error) {
	// Only one renderer is expected to have a current candidate. Prefer web
	// when it reports one, otherwise inspect media.
	if c.Web != nil {
		status, err := c.Web.Current(ctx)
		if err != nil {
			return Status{}, err
		}
		if status.ReleaseDigest != "" || status.Healthy {
			return status, nil
		}
	}
	if c.Media != nil {
		return c.Media.Current(ctx)
	}
	return Status{}, nil
}

func (c *Composite) Capture(ctx context.Context) ([]byte, error) {
	for _, adapter := range []Adapter{c.Web, c.Media} {
		if adapter == nil {
			continue
		}
		status, err := adapter.Current(ctx)
		if err != nil {
			return nil, err
		}
		if status.ReleaseDigest == "" && !status.Healthy {
			continue
		}
		capture, ok := adapter.(interface {
			Capture(context.Context) ([]byte, error)
		})
		if !ok {
			return nil, screenshot.ErrUnsupported
		}
		return capture.Capture(ctx)
	}
	return nil, screenshot.ErrUnsupported
}
