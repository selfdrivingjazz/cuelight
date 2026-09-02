package usage

import (
	"context"
	"errors"
	"fmt"
)

const ReadAction = "usage.read"

type Service struct {
	Repository Repository
	Authorizer Authorizer
}

func NewService(repository Repository, authorizer Authorizer) *Service {
	return &Service{Repository: repository, Authorizer: authorizer}
}

func (s Service) authorize(ctx context.Context, q Query) error {
	if s.Repository == nil {
		return errors.New("usage repository is required")
	}
	if s.Authorizer == nil {
		return nil
	}
	if err := s.Authorizer.Authorize(ctx, q.ActorID, ReadAction, string(q.Kind), q.ResourceID); err != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	return nil
}

// Usage authorizes and executes one bounded usage lookup. Authorization is
// intentionally performed before any source existence or relation query.
func (s Service) Usage(ctx context.Context, query Query) (Page, error) {
	query, err := query.Normalize()
	if err != nil {
		return Page{}, err
	}
	if err := s.authorize(ctx, query); err != nil {
		return Page{}, err
	}
	result, err := s.Repository.Usage(ctx, query)
	if err != nil {
		return Page{}, err
	}
	if result.ResourceID == "" {
		result.ResourceID = query.ResourceID
	}
	if result.References == nil {
		result.References = []Reference{}
	}
	return result, nil
}

// GetUsage is an explicit alias for callers that use repository terminology.
func (s Service) GetUsage(ctx context.Context, query Query) (Page, error) {
	return s.Usage(ctx, query)
}

// Get is a descriptive alias for callers that prefer read semantics.
func (s Service) Get(ctx context.Context, query Query) (Page, error) {
	return s.Usage(ctx, query)
}

// List is retained as a descriptive alias for collection-oriented adapters.
func (s Service) List(ctx context.Context, query Query) (Page, error) {
	return s.Usage(ctx, query)
}
