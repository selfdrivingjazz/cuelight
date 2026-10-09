package playerapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	"github.com/jmilldotdev/cuelight/internal/player/client"
	canonical "github.com/jmilldotdev/cuelight/internal/release"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type fakeRepository struct {
	assignment      assignment.Assignment
	hasAssignment   bool
	capabilities    []string
	authorized      map[string]bool
	report          client.NodeReport
	reportAccepted  bool
	events          client.EventBatch
	acknowledgement client.EventAcknowledgement
	eventErr        error
	wait            func(context.Context) error
}

func (f *fakeRepository) PutCapabilities(_ context.Context, node string, _ client.CapabilityReport) error {
	f.capabilities = append(f.capabilities, node)
	return nil
}
func (f *fakeRepository) CurrentAssignment(_ context.Context, node string) (assignment.Assignment, bool, error) {
	if !f.hasAssignment || f.assignment.NodeID != node {
		return assignment.Assignment{}, false, nil
	}
	return f.assignment, true, nil
}
func (f *fakeRepository) WaitForAssignment(ctx context.Context, _ string, _ int64, _ time.Duration) error {
	if f.wait != nil {
		return f.wait(ctx)
	}
	return nil
}
func (f *fakeRepository) AssignedRelease(_ context.Context, node, releaseID string) (assignment.Assignment, bool, error) {
	if f.assignment.NodeID != node || f.assignment.ReleaseID != releaseID {
		return assignment.Assignment{}, false, nil
	}
	return f.assignment, f.hasAssignment, nil
}
func (f *fakeRepository) AuthorizeArtifact(_ context.Context, node, digest string) (bool, error) {
	return f.authorized[node+":"+digest], nil
}
func (f *fakeRepository) RecordReport(_ context.Context, node string, report client.NodeReport) (bool, error) {
	f.report = report
	f.reportAccepted = node == "node-a"
	return f.reportAccepted, nil
}
func (f *fakeRepository) AppendEvents(_ context.Context, _ string, batch client.EventBatch) (client.EventAcknowledgement, error) {
	f.events = batch
	return f.acknowledgement, f.eventErr
}

type fakeLoader struct{ envelope canonical.EnvelopeV1 }

func (f fakeLoader) LoadRelease(_ context.Context, _ string) (canonical.EnvelopeV1, error) {
	return f.envelope, nil
}

type fakeCAS struct{ body string }

func (f fakeCAS) Stat(_ context.Context, digest string) (ArtifactStat, error) {
	return ArtifactStat{Digest: digest, Size: int64(len(f.body))}, nil
}
func (f fakeCAS) Open(_ context.Context, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func testService(repo *fakeRepository) *Service {
	service, err := NewService(repo, fakeLoader{envelope: canonical.EnvelopeV1{ID: "release-a", Digest: testDigest}}, fakeCAS{body: "abcdef"}, Limits{MaxBodyBytes: 128, MaxEvents: 3, MaxWait: 30 * time.Second})
	if err != nil {
		panic(err)
	}
	service.Identity = func(*http.Request) (string, error) { return "node-a", nil }
	return service
}
func validCapabilities() client.CapabilityReport {
	return client.CapabilityReport{Platform: "linux", Architecture: "amd64", ProtocolVersion: "1.0", RendererVersion: "1", ProfileID: "default"}
}
func validAssignment() assignment.Assignment {
	return assignment.Assignment{ID: "assignment-a", NodeID: "node-a", ReleaseID: "release-a", ReleaseDigest: testDigest, DesiredGeneration: 4}
}

func TestPlayerCapabilitiesUsesCertificateIdentityNotHeader(t *testing.T) {
	repo := &fakeRepository{}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/player/capabilities", strings.NewReader(`{"platform":"linux","architecture":"amd64","protocolVersion":"1.0","rendererVersion":"1","profileId":"default"}`))
	request.Header.Set("X-Node-ID", "node-b")
	NewHandler(testService(repo)).ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(repo.capabilities) != 1 || repo.capabilities[0] != "node-a" {
		t.Fatalf("identity spoofing result: status=%d nodes=%v", response.Code, repo.capabilities)
	}
}

func TestAssignmentConditional304AndLongPollCancellation(t *testing.T) {
	repo := &fakeRepository{assignment: validAssignment(), hasAssignment: true}
	service := testService(repo)
	result, err := service.GetAssignment(context.Background(), "node-a", `"assignment-4"`, 0)
	if err != nil || !result.NotModified {
		t.Fatalf("conditional assignment = %#v, %v", result, err)
	}
	repo.wait = func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = service.GetAssignment(ctx, "node-a", `"assignment-4"`, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled long poll error = %v", err)
	}
}

func TestAssignmentCarriesReleasePresentationKind(t *testing.T) {
	repo := &fakeRepository{assignment: validAssignment(), hasAssignment: true}
	service, err := NewService(repo, fakeLoader{envelope: canonical.EnvelopeV1{
		ID:     "release-a",
		Digest: testDigest,
		Payload: canonical.PayloadV1{
			SchemaVersion: canonical.SchemaVersion,
			Presentation: canonical.Presentation{
				Kind: canonical.PresentationWeb,
				Web:  &canonical.WebPresentation{URL: "https://example.com/board"},
			},
		},
	}}, fakeCAS{body: "abcdef"}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.GetAssignment(context.Background(), "node-a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Assignment.PresentationKind != canonical.PresentationWeb {
		t.Fatalf("presentation kind = %q", result.Assignment.PresentationKind)
	}
}

func TestPlayerReleaseAndArtifactCrossNodeAuthorization(t *testing.T) {
	repo := &fakeRepository{assignment: validAssignment(), hasAssignment: true, authorized: map[string]bool{"node-a:" + testDigest: true}}
	service := testService(repo)
	if _, err := service.FetchRelease(context.Background(), "node-b", "release-a"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-node release error = %v", err)
	}
	if _, err := service.HeadArtifact(context.Background(), "node-b", testDigest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-node artifact error = %v", err)
	}
}

func TestReportDoesNotMutateDesiredAssignment(t *testing.T) {
	repo := &fakeRepository{assignment: validAssignment(), hasAssignment: true, reportAccepted: true}
	service := testService(repo)
	accepted, err := service.Report(context.Background(), "node-a", client.NodeReport{EventEpoch: "epoch-a", Sequence: 1, DesiredGenerationSeen: 4, ObservedGeneration: 4})
	if err != nil || !accepted {
		t.Fatalf("report = %v, %v", accepted, err)
	}
	if repo.assignment.DesiredGeneration != 4 {
		t.Fatal("report changed desired generation")
	}
}

func TestEventsRejectConflictsAndPreserveWatermarkEpoch(t *testing.T) {
	repo := &fakeRepository{acknowledgement: client.EventAcknowledgement{Epoch: "epoch-a", AcknowledgedThrough: 3}}
	service := testService(repo)
	duplicate := client.EventBatch{Epoch: "epoch-a", Events: []client.NodeEvent{{ID: "event-1", Epoch: "epoch-a", Sequence: 1, Type: "one"}, {ID: "event-1", Epoch: "epoch-a", Sequence: 1, Type: "one"}}}
	if _, err := service.AppendEvents(context.Background(), "node-a", duplicate); err != nil {
		t.Fatalf("duplicate event error = %v", err)
	}
	batch := client.EventBatch{Epoch: "epoch-a", Events: []client.NodeEvent{{ID: "event-1", Epoch: "epoch-a", Sequence: 2, Type: "one"}, {ID: "event-2", Epoch: "epoch-a", Sequence: 2, Type: "two"}}}
	if _, err := service.AppendEvents(context.Background(), "node-a", batch); !errors.Is(err, ErrEventConflict) {
		t.Fatalf("conflicting event error = %v", err)
	}
	batch.Events = []client.NodeEvent{{ID: "event-1", Epoch: "epoch-a", Sequence: 1, Type: "one"}}
	ack, err := service.AppendEvents(context.Background(), "node-a", batch)
	if err != nil || ack.Epoch != "epoch-a" || ack.AcknowledgedThrough != 3 {
		t.Fatalf("event ack = %#v, %v", ack, err)
	}
}

func TestEventsPropagateGapAndOverflowEpochErrors(t *testing.T) {
	repo := &fakeRepository{eventErr: ErrEventGap}
	service := testService(repo)
	batch := client.EventBatch{Epoch: "epoch-a", Events: []client.NodeEvent{{ID: "event-1", Epoch: "epoch-a", Sequence: 2, Type: "one"}}}
	if _, err := service.AppendEvents(context.Background(), "node-a", batch); !errors.Is(err, ErrEventGap) {
		t.Fatalf("gap error = %v", err)
	}
	repo.eventErr = ErrEpochMismatch
	if _, err := service.AppendEvents(context.Background(), "node-a", batch); !errors.Is(err, ErrEpochMismatch) {
		t.Fatalf("overflow epoch error = %v", err)
	}
}

func TestArtifactHeadRangeIfRangeFallbackAnd416(t *testing.T) {
	repo := &fakeRepository{authorized: map[string]bool{"node-a:" + testDigest: true}}
	handler := NewHandler(testService(repo))
	request := httptest.NewRequest(http.MethodHead, "/player/artifacts/"+testDigest+"/content", nil)
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, request)
	if head.Code != http.StatusOK || head.Header().Get("Content-Length") != "6" || head.Header().Get("ETag") == "" {
		t.Fatalf("HEAD = %d headers=%v", head.Code, head.Header())
	}
	request = httptest.NewRequest(http.MethodGet, "/player/artifacts/"+testDigest+"/content", nil)
	request.Header.Set("Range", "bytes=1-3")
	request.Header.Set("If-Range", head.Header().Get("ETag"))
	partial := httptest.NewRecorder()
	handler.ServeHTTP(partial, request)
	if partial.Code != http.StatusPartialContent || partial.Body.String() != "bcd" || partial.Header().Get("Content-Range") != "bytes 1-3/6" {
		t.Fatalf("range = %d body=%q headers=%v", partial.Code, partial.Body.String(), partial.Header())
	}
	request = httptest.NewRequest(http.MethodGet, "/player/artifacts/"+testDigest+"/content", nil)
	request.Header.Set("Range", "bytes=1-3")
	request.Header.Set("If-Range", `"stale"`)
	full := httptest.NewRecorder()
	handler.ServeHTTP(full, request)
	if full.Code != http.StatusOK || full.Body.String() != "abcdef" {
		t.Fatalf("If-Range fallback = %d body=%q", full.Code, full.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/player/artifacts/"+testDigest+"/content", nil)
	request.Header.Set("Range", "bytes=99-100")
	unsatisfiable := httptest.NewRecorder()
	handler.ServeHTTP(unsatisfiable, request)
	if unsatisfiable.Code != http.StatusRequestedRangeNotSatisfiable || unsatisfiable.Header().Get("Content-Range") != "bytes */6" {
		t.Fatalf("416 = %d headers=%v", unsatisfiable.Code, unsatisfiable.Header())
	}
}

func TestPlayerBodyLimit(t *testing.T) {
	repo := &fakeRepository{}
	service := testService(repo)
	service.Limits.MaxBodyBytes = 8
	request := httptest.NewRequest(http.MethodPut, "/player/capabilities", strings.NewReader(`{"platform":"linux"}`))
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit status = %d", response.Code)
	}
}
