package operatorapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jmill/cable/internal/control/cable"
)

type cableHandlerRepository struct {
	deployment cable.CableDeployment
	mutation   cable.Mutation
}

func (f *cableHandlerRepository) CreateDeployment(_ context.Context, m cable.Mutation, _ cable.DeploymentInput) (cable.CableDeployment, error) {
	f.mutation = m
	return f.deployment, nil
}
func (f *cableHandlerRepository) ListDeployments(context.Context, string, int) (cable.DeploymentPage, error) {
	return cable.DeploymentPage{Items: []cable.CableDeployment{f.deployment}, Page: cable.Page{}}, nil
}
func (f *cableHandlerRepository) Deployment(context.Context, string) (cable.CableDeployment, error) {
	return f.deployment, nil
}
func (f *cableHandlerRepository) UpdateDeployment(_ context.Context, m cable.Mutation, _ string, _ cable.DeploymentInput) (cable.CableDeployment, error) {
	f.mutation = m
	return f.deployment, nil
}
func (f *cableHandlerRepository) DeleteDeployment(_ context.Context, m cable.Mutation, _ string) error {
	f.mutation = m
	return nil
}
func (f *cableHandlerRepository) CreateChannel(context.Context, cable.Mutation, cable.ChannelInput) (cable.Channel, error) {
	return cable.Channel{}, errors.New("unused")
}
func (f *cableHandlerRepository) Channel(context.Context, string) (cable.Channel, error) {
	return cable.Channel{}, errors.New("unused")
}
func (f *cableHandlerRepository) UpdateChannel(context.Context, cable.Mutation, string, cable.ChannelInput) (cable.Channel, error) {
	return cable.Channel{}, errors.New("unused")
}
func (f *cableHandlerRepository) ListChannels(context.Context, string, string, int) (cable.ChannelPage, error) {
	return cable.ChannelPage{}, errors.New("unused")
}
func (f *cableHandlerRepository) DeleteChannel(context.Context, cable.Mutation, string) error {
	return errors.New("unused")
}
func (f *cableHandlerRepository) CreateProgramEntry(context.Context, cable.Mutation, cable.ProgramInput) (cable.ProgramEntry, error) {
	return cable.ProgramEntry{}, errors.New("unused")
}
func (f *cableHandlerRepository) ProgramEntry(context.Context, string) (cable.ProgramEntry, error) {
	return cable.ProgramEntry{}, errors.New("unused")
}
func (f *cableHandlerRepository) UpdateProgramEntry(context.Context, cable.Mutation, string, cable.ProgramInput) (cable.ProgramEntry, error) {
	return cable.ProgramEntry{}, errors.New("unused")
}
func (f *cableHandlerRepository) ListProgramEntries(context.Context, string, string, int) (cable.ProgramPage, error) {
	return cable.ProgramPage{}, errors.New("unused")
}
func (f *cableHandlerRepository) DeleteProgramEntry(context.Context, cable.Mutation, string) error {
	return errors.New("unused")
}
func TestCableHandlerMutationContract(t *testing.T) {
	repo := &cableHandlerRepository{deployment: cable.CableDeployment{ID: "dep", Name: "demo", Version: 7}}
	h := NewHandler(Dependencies{
		Auth:            testAuth{scopes: []string{ScopeCableRead, ScopeCableWrite}},
		Cable:           &cable.Service{Repository: repo},
		CableRepository: repo,
	})
	w := withHeader(h, http.MethodPost, "/cable/deployments", "token", strings.NewReader(`{"name":"demo"}`), map[string]string{"Idempotency-Key": "0123456789abcdef"})
	if w.Code != http.StatusCreated || w.Header().Get("ETag") != `"v7"` || repo.mutation.ActorID != "actor" || repo.mutation.IdempotencyKey == "" {
		t.Fatalf("create status=%d etag=%q mutation=%+v", w.Code, w.Header().Get("ETag"), repo.mutation)
	}
	w = request(h, http.MethodPatch, "/cable/deployments/dep", "token", strings.NewReader(`{"name":"next"}`))
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("missing If-Match status=%d", w.Code)
	}
	w = withHeader(h, http.MethodPatch, "/cable/deployments/dep", "token", strings.NewReader(`{"name":"next"}`), map[string]string{"If-Match": `"v7"`})
	if w.Code != http.StatusOK || repo.mutation.ExpectedVersion != 7 {
		t.Fatalf("update status=%d mutation=%+v", w.Code, repo.mutation)
	}
}

func TestCableHandlerListEnvelopeAndAuth(t *testing.T) {
	repo := &cableHandlerRepository{deployment: cable.CableDeployment{ID: "dep", Version: 2}}
	h := NewHandler(Dependencies{Auth: testAuth{scopes: []string{ScopeCableRead}}, CableRepository: repo})
	w := request(h, http.MethodGet, "/cable/deployments?limit=1", "token", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"items"`) || !strings.Contains(w.Body.String(), `"page"`) {
		t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
	}
	w = request(h, http.MethodGet, "/cable/deployments", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", w.Code)
	}
}
