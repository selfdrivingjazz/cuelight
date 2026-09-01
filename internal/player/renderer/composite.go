package renderer

import (
	"context"
	"errors"

	"github.com/jmill/cable/internal/release"
)

// Composite dispatches a materialized candidate to the adapter that owns its
// presentation kind. Empty kind is treated as media for legacy in-memory
// candidates; signed releases always carry an explicit discriminator.
type Composite struct {
	Media Adapter
	Cable Adapter
}

func NewComposite(media, cable Adapter) (*Composite, error) {
	if media == nil || cable == nil {
		return nil, errors.New("media and cable renderers are required")
	}
	return &Composite{Media: media, Cable: cable}, nil
}

func (c *Composite) adapter(candidate Candidate) (Adapter, error) {
	switch candidate.Kind {
	case "", release.PresentationMedia, release.PresentationMediaSchedule:
		return c.Media, nil
	case release.PresentationCable:
		return c.Cable, nil
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
	if c.Cable != nil {
		result = errors.Join(result, c.Cable.Stop(ctx))
	}
	return result
}
func (c *Composite) Current(ctx context.Context) (Status, error) {
	// Only one renderer is expected to have a current candidate. Prefer cable
	// when it reports one, otherwise inspect media.
	if c.Cable != nil {
		status, err := c.Cable.Current(ctx)
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
