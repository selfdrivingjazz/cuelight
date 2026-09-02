package store

import (
	"context"

	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/page"
)

// OperatorAdapter forwards authored media operations while keeping the
// controller composition layer independent from SQL implementation details.
func (a *OperatorAdapter) CreateMedia(ctx context.Context, mutation media.Mutation, value media.Media) (media.Media, error) {
	return a.Store.CreateMedia(ctx, mutation, value)
}
func (a *OperatorAdapter) ListMedia(ctx context.Context, request page.PageRequest, filter media.MediaListFilter) (page.Response[media.Media], error) {
	return a.Store.ListMedia(ctx, request, filter)
}
func (a *OperatorAdapter) GetMedia(ctx context.Context, id string) (media.Media, error) {
	return a.Store.GetMedia(ctx, id)
}
func (a *OperatorAdapter) UpdateMedia(ctx context.Context, mutation media.Mutation, id string, expectedVersion int64, patch media.MediaPatch) (media.Media, error) {
	return a.Store.UpdateMedia(ctx, mutation, id, expectedVersion, patch)
}
func (a *OperatorAdapter) GetMediaUsage(ctx context.Context, id string) (media.MediaUsage, error) {
	return a.Store.GetMediaUsage(ctx, id)
}

var _ media.MediaRepository = (*OperatorAdapter)(nil)
