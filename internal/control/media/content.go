package media

import (
	"context"

	"github.com/jmill/cable/internal/media/localcas"
)

// LocalCASContent exposes only digest-addressed reads. CAS filesystem paths
// never cross this boundary or appear in an HTTP response.
type LocalCASContent struct{ Store *localcas.Store }

func NewLocalCASContent(store *localcas.Store) LocalCASContent { return LocalCASContent{Store: store} }

func (c LocalCASContent) Stat(ctx context.Context, digest string) (ContentObject, error) {
	if err := ctx.Err(); err != nil {
		return ContentObject{}, err
	}
	if c.Store == nil {
		return ContentObject{}, ErrInvalidArgument
	}
	object, err := c.Store.Stat(digest)
	if err != nil {
		return ContentObject{}, err
	}
	return ContentObject{Digest: object.Digest, Size: object.Size}, nil
}

func (c LocalCASContent) Open(ctx context.Context, digest string) (ContentHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.Store == nil {
		return nil, ErrInvalidArgument
	}
	return c.Store.Open(digest)
}

var _ ContentReader = LocalCASContent{}
