package usage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jmilldotdev/cuelight/internal/control/auth"
)

type testRepository struct {
	calls int
	query Query
	page  Page
	err   error
}

func (r *testRepository) Usage(_ context.Context, query Query) (Page, error) {
	r.calls++
	r.query = query
	return r.page, r.err
}

type testAuthorizer struct {
	calls  int
	actor  string
	action string
	kind   string
	id     string
	err    error
}

func (a *testAuthorizer) Authorize(_ context.Context, actor, action, kind, id string) error {
	a.calls++
	a.actor, a.action, a.kind, a.id = actor, action, kind, id
	return a.err
}

func TestUsageAuthorizesBeforeRepository(t *testing.T) {
	repository := &testRepository{page: Page{References: []Reference{{Kind: KindPlaylist, ID: "p1"}}}}
	authorizer := &testAuthorizer{err: errors.New("no access")}
	service := Service{Repository: repository, Authorizer: authorizer}

	_, err := service.Usage(context.Background(), Query{ActorID: "actor-1", Kind: KindMedia, ResourceID: "m1", Limit: 1})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error = %v, want forbidden", err)
	}
	if repository.calls != 0 {
		t.Fatalf("repository calls = %d, want zero after denied authorization", repository.calls)
	}
	if authorizer.calls != 1 || authorizer.actor != "actor-1" || authorizer.action != ReadAction || authorizer.kind != "media" || authorizer.id != "m1" {
		t.Fatalf("authorization call = %#v, want actor/action/resource", authorizer)
	}
}

func TestUsageNormalizesAndBoundsQuery(t *testing.T) {
	repository := &testRepository{page: Page{ResourceID: "m1"}}
	service := Service{Repository: repository}

	result, err := service.Usage(context.Background(), Query{Kind: Kind(" MEDIA "), ResourceID: " m1 "})
	if err != nil {
		t.Fatal(err)
	}
	if result.ResourceID != "m1" || repository.calls != 1 {
		t.Fatalf("result=%#v calls=%d", result, repository.calls)
	}
	if repository.query.Kind != KindMedia || repository.query.ResourceID != "m1" || repository.query.Limit != 25 {
		t.Fatalf("normalized query = %#v", repository.query)
	}
	if result.References == nil {
		t.Fatal("references must be an empty array, not null")
	}

	for _, query := range []Query{
		{Kind: KindMedia, ResourceID: "m1", Limit: -1},
		{Kind: KindMedia, ResourceID: "m1", Limit: 101},
		{Kind: Kind("unknown"), ResourceID: "m1"},
		{Kind: KindMedia},
	} {
		if _, err := service.Usage(context.Background(), query); err == nil {
			t.Errorf("query %#v unexpectedly succeeded", query)
		}
	}
	if repository.calls != 1 {
		t.Fatalf("invalid queries reached repository: %d calls", repository.calls)
	}
}

func TestHandlerUsesAuthenticatedActorAndReturnsReferences(t *testing.T) {
	repository := &testRepository{page: Page{References: []Reference{{Kind: KindPlaylist, ID: "p1", Name: "Morning", Href: "/library/playlists/p1"}}}}
	authorizer := &testAuthorizer{}
	handler := NewHandler(&Service{Repository: repository, Authorizer: authorizer})

	request := httptest.NewRequest(http.MethodGet, "/usage/media/m1?limit=5", nil)
	request = request.WithContext(auth.WithActor(request.Context(), auth.Actor{ID: "actor-2"}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if authorizer.actor != "actor-2" || repository.query.Limit != 5 {
		t.Fatalf("actor/query = %q/%#v", authorizer.actor, repository.query)
	}
	if got := response.Body.String(); got == "" || !contains(got, `"kind":"playlist"`) {
		t.Fatalf("body = %q", got)
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
