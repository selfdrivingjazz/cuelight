package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	playerclient "github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/release"
)

type webLauncher struct {
	launches chan WebLaunch
	session  *inputSession
}

func (l *webLauncher) Launch(context.Context, string, string) (BrowserSession, error) {
	panic("web candidates must not use bridge launches")
}

func (l *webLauncher) LaunchWeb(_ context.Context, launch WebLaunch) (BrowserSession, error) {
	l.launches <- launch
	return l.session, nil
}

type inputSession struct {
	capturingBrowserSession
	batches chan []playerclient.RemoteEvent
}

func (s *inputSession) SendInput(_ context.Context, events []playerclient.RemoteEvent) error {
	s.batches <- events
	return nil
}

type scriptedRemote struct {
	mu     sync.Mutex
	pages  []playerclient.RemoteEventPage
	afters []int64
}

func (r *scriptedRemote) PollRemoteEvents(ctx context.Context, sessionID string, after int64, _ time.Duration) (playerclient.RemoteEventPage, error) {
	r.mu.Lock()
	r.afters = append(r.afters, after)
	if sessionID == "remote-one" && len(r.pages) > 0 {
		page := r.pages[0]
		r.pages = r.pages[1:]
		r.mu.Unlock()
		return page, nil
	}
	r.mu.Unlock()
	<-ctx.Done()
	return playerclient.RemoteEventPage{}, ctx.Err()
}

func TestChromiumWebForwardsPolledRemoteEvents(t *testing.T) {
	session := &inputSession{capturingBrowserSession: capturingBrowserSession{alive: true}, batches: make(chan []playerclient.RemoteEvent, 4)}
	launcher := &webLauncher{launches: make(chan WebLaunch, 1), session: session}
	remote := &scriptedRemote{pages: []playerclient.RemoteEventPage{
		{Events: []playerclient.RemoteEvent{{Sequence: 1, Type: "pointer", X: 0.5, Y: 0.25, State: "click"}}, NextSequence: 1},
		{Events: []playerclient.RemoteEvent{{Sequence: 2, Type: "text", Text: "hi"}, {Sequence: 3, Type: "key", Key: "Enter"}}, NextSequence: 3},
	}}
	adapter, err := NewChromium(ChromiumConfig{RuntimeRoot: t.TempDir(), Timeout: 2 * time.Second, Launcher: launcher, Remote: remote})
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Kind: release.PresentationWeb, ReleaseDigest: "web-digest", WebURL: "https://example.com/", Remote: &RemoteDescriptor{SessionID: "remote-one", URL: "https://control.example/remote/x", ShowQR: true}}
	if err := adapter.Prepare(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	launch := <-launcher.launches
	if launch.URL != "https://example.com/" || launch.Remote == nil || !launch.Remote.ShowQR {
		t.Fatalf("launch = %#v", launch)
	}
	if err := adapter.Commit(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	status, err := adapter.Current(context.Background())
	if err != nil || !status.Healthy {
		t.Fatalf("status = %#v, %v", status, err)
	}
	var sequences []int64
	for len(sequences) < 3 {
		select {
		case batch := <-session.batches:
			for _, event := range batch {
				sequences = append(sequences, event.Sequence)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("forwarded %v", sequences)
		}
	}
	if err := adapter.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	remote.mu.Lock()
	afters := append([]int64(nil), remote.afters...)
	remote.mu.Unlock()
	if len(afters) < 3 || afters[0] != 0 || afters[1] != 1 || afters[2] != 3 {
		t.Fatalf("poll cursors = %v", afters)
	}
	if !session.stopped {
		t.Fatal("web session was not stopped")
	}
}

// renewingRemote serves one page from each session, then reports the session
// gone; CreateRemoteSession issues the replacement.
type renewingRemote struct {
	mu      sync.Mutex
	polls   []string
	afters  []int64
	served  map[string]bool
	creates []int64
	fail    int
}

func (r *renewingRemote) PollRemoteEvents(ctx context.Context, sessionID string, after int64, _ time.Duration) (playerclient.RemoteEventPage, error) {
	r.mu.Lock()
	r.polls = append(r.polls, sessionID)
	r.afters = append(r.afters, after)
	if !r.served[sessionID] {
		r.served[sessionID] = true
		r.mu.Unlock()
		next := after + 1
		return playerclient.RemoteEventPage{Events: []playerclient.RemoteEvent{{Sequence: next, Type: "key", Key: "Enter"}}, NextSequence: next}, nil
	}
	gone := sessionID == "remote-one"
	r.mu.Unlock()
	if gone {
		return playerclient.RemoteEventPage{}, fmt.Errorf("%w: HTTP 410", playerclient.ErrRemoteSessionGone)
	}
	<-ctx.Done()
	return playerclient.RemoteEventPage{}, ctx.Err()
}

func (r *renewingRemote) CreateRemoteSession(_ context.Context, generation int64) (playerclient.RemoteSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.creates = append(r.creates, generation)
	if r.fail > 0 {
		r.fail--
		return playerclient.RemoteSession{}, errors.New("control plane unavailable")
	}
	return playerclient.RemoteSession{ID: "remote-two", RemoteURL: "https://control.example/remote/two", ShowQR: true, ExpiresAt: time.Unix(5000, 0).UTC()}, nil
}

type updatingSession struct {
	inputSession
	updates chan RemoteDescriptor
}

func (s *updatingSession) UpdateRemote(_ context.Context, remote RemoteDescriptor) error {
	s.updates <- remote
	return nil
}

type updatingLauncher struct {
	capturingBrowserLauncher
	session *updatingSession
}

func (l *updatingLauncher) LaunchWeb(context.Context, WebLaunch) (BrowserSession, error) {
	return l.session, nil
}

func TestChromiumWebRenewsGoneRemoteSession(t *testing.T) {
	session := &updatingSession{inputSession: inputSession{capturingBrowserSession: capturingBrowserSession{alive: true}, batches: make(chan []playerclient.RemoteEvent, 4)}, updates: make(chan RemoteDescriptor, 1)}
	launcher := &updatingLauncher{session: session}
	remote := &renewingRemote{served: map[string]bool{}}
	adapter, err := NewChromium(ChromiumConfig{RuntimeRoot: t.TempDir(), Timeout: 2 * time.Second, Launcher: launcher, Remote: remote})
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Kind: release.PresentationWeb, ReleaseDigest: "web-digest", WebURL: "https://example.com/", Remote: &RemoteDescriptor{SessionID: "remote-one", Generation: 7, URL: "https://control.example/remote/one", ShowQR: true}}
	if err := adapter.Prepare(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(context.Background())
	var sequences []int64
	for len(sequences) < 2 {
		select {
		case batch := <-session.batches:
			for _, event := range batch {
				sequences = append(sequences, event.Sequence)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("forwarded %v", sequences)
		}
	}
	if sequences[0] != 1 || sequences[1] != 2 {
		t.Fatalf("sequences = %v", sequences)
	}
	select {
	case update := <-session.updates:
		if update.SessionID != "remote-two" || update.URL != "https://control.example/remote/two" || !update.ShowQR || update.Generation != 7 {
			t.Fatalf("update = %#v", update)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("QR overlay was not refreshed")
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	// The replacement session is polled with the cursor held before renewal.
	if len(remote.creates) != 1 || remote.creates[0] != 7 || len(remote.polls) < 3 || remote.polls[2] != "remote-two" || remote.afters[2] != 1 {
		t.Fatalf("creates=%v polls=%v afters=%v", remote.creates, remote.polls, remote.afters)
	}
}

type guideTokens struct {
	deployments []string
}

func (g *guideTokens) CreateCableGuideToken(_ context.Context, deploymentID string) (playerclient.CableGuideToken, error) {
	g.deployments = append(g.deployments, deploymentID)
	return playerclient.CableGuideToken{Token: "guide/token+1", ExpiresAt: time.Now().Add(time.Hour)}, nil
}

// startWebCandidate runs a web candidate with an active remote session and
// returns the launch plus the remote poll cursors observed shortly after.
func startWebCandidate(t *testing.T, webURL string, tokens *guideTokens) (WebLaunch, *inputSession, *scriptedRemote) {
	t.Helper()
	session := &inputSession{capturingBrowserSession: capturingBrowserSession{alive: true}, batches: make(chan []playerclient.RemoteEvent, 4)}
	launcher := &webLauncher{launches: make(chan WebLaunch, 1), session: session}
	remote := &scriptedRemote{pages: []playerclient.RemoteEventPage{{Events: []playerclient.RemoteEvent{{Sequence: 1, Type: "key", Key: "Enter"}}, NextSequence: 1}}}
	adapter, err := NewChromium(ChromiumConfig{RuntimeRoot: t.TempDir(), Timeout: 2 * time.Second, Launcher: launcher, Remote: remote, GuideTokens: tokens})
	if err != nil {
		t.Fatal(err)
	}
	candidate := Candidate{Kind: release.PresentationWeb, ReleaseDigest: "web-digest", WebURL: webURL, Remote: &RemoteDescriptor{SessionID: "remote-one", URL: "https://control.example/remote/x", ShowQR: true}}
	if err := adapter.Prepare(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Stop(context.Background()) })
	return <-launcher.launches, session, remote
}

func TestChromiumHostedGuideGetsTokenAndNoNativeInput(t *testing.T) {
	// The guide's public origin differs from the player's control address
	// (players often reach control through a tunnel); it is still a guide.
	tokens := &guideTokens{}
	launch, session, remote := startWebCandidate(t, "https://control.example:443/cable/?deployment=dep-1#stale", tokens)
	if launch.URL != "https://control.example:443/cable/?deployment=dep-1#token=guide%2Ftoken%2B1" {
		t.Fatalf("guide URL = %q", launch.URL)
	}
	if launch.Remote != nil {
		t.Fatalf("guide draws its own remote QR; overlay = %#v", launch.Remote)
	}
	if len(tokens.deployments) != 1 || tokens.deployments[0] != "dep-1" {
		t.Fatalf("minted for %v", tokens.deployments)
	}
	select {
	case batch := <-session.batches:
		t.Fatalf("guide received native input %v", batch)
	case <-time.After(100 * time.Millisecond):
	}
	remote.mu.Lock()
	defer remote.mu.Unlock()
	if len(remote.afters) != 0 {
		t.Fatalf("player polled the guide's remote session: %v", remote.afters)
	}
}

func TestChromiumPlainWebPageIsUnchanged(t *testing.T) {
	for _, webURL := range []string{
		"http://control.example/cable/?deployment=dep-1",   // not https
		"https://control.example/cable/",                   // no deployment
		"https://control.example/cable/x?deployment=dep-1", // not the guide path
	} {
		t.Run(webURL, func(t *testing.T) {
			tokens := &guideTokens{}
			launch, session, _ := startWebCandidate(t, webURL, tokens)
			if launch.URL != webURL || launch.Remote == nil || len(tokens.deployments) != 0 {
				t.Fatalf("launch = %#v minted = %v", launch, tokens.deployments)
			}
			select {
			case <-session.batches:
			case <-time.After(2 * time.Second):
				t.Fatal("plain web page did not receive remote input")
			}
		})
	}
}

func TestChromiumWebRequiresWebLauncher(t *testing.T) {
	adapter, err := NewChromium(ChromiumConfig{RuntimeRoot: t.TempDir(), Launcher: &capturingBrowserLauncher{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Prepare(context.Background(), Candidate{Kind: release.PresentationWeb, ReleaseDigest: "d", WebURL: "https://example.com/"}); err == nil {
		t.Fatal("bridge-only launcher accepted a web candidate")
	}
}

// recordingLauncher records every window the adapter opens, in order.
type recordingLauncher struct {
	mu       sync.Mutex
	opened   []string
	sessions []*capturingBrowserSession
}

func (l *recordingLauncher) open(entry string) BrowserSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	session := &capturingBrowserSession{alive: true}
	l.opened, l.sessions = append(l.opened, entry), append(l.sessions, session)
	return session
}

func (l *recordingLauncher) Launch(_ context.Context, target, _ string) (BrowserSession, error) {
	return l.open("bridge " + target), nil
}

func (l *recordingLauncher) LaunchWeb(_ context.Context, launch WebLaunch) (BrowserSession, error) {
	return l.open("web " + launch.URL), nil
}

func TestChromiumScheduleShowsWebItemForItsDuration(t *testing.T) {
	source := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(source, []byte("clip"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &recordingLauncher{}
	adapter, err := NewChromium(ChromiumConfig{RuntimeRoot: t.TempDir(), Timeout: 2 * time.Second, Launcher: launcher})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	switchAt := now.Add(300 * time.Millisecond)
	candidate := Candidate{Kind: release.PresentationMediaSchedule, ReleaseDigest: "schedule-digest", WindowStart: now.Add(-time.Second), WindowEnd: now.Add(time.Hour), Schedule: []ScheduleItem{
		{ID: "page", MediaID: "media-page", StartsAt: now.Add(-time.Second), EndsAt: switchAt, WebURL: "https://example.com/page"},
		{ID: "clip", MediaID: "media-clip", StartsAt: switchAt, EndsAt: now.Add(time.Hour), Source: source, MediaType: "video/mp4", Fit: "fit", Volume: 50},
	}}
	if err := adapter.Prepare(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop(context.Background())
	status, err := adapter.Health(context.Background(), candidate)
	if err != nil || !status.Healthy || status.Current == nil || status.Current.MediaID != "media-page" {
		t.Fatalf("web item health = %#v, %v", status, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		launcher.mu.Lock()
		opened := slices.Clone(launcher.opened)
		first := launcher.sessions[0]
		launcher.mu.Unlock()
		if len(opened) == 2 {
			if opened[0] != "web https://example.com/page" || !strings.HasPrefix(opened[1], "bridge http://127.0.0.1:") || !strings.HasSuffix(opened[1], "/items/clip.html") {
				t.Fatalf("opened = %v", opened)
			}
			first.mu.Lock()
			stopped := first.stopped
			first.mu.Unlock()
			if !stopped {
				t.Fatal("web item window was not closed at its end")
			}
			page, err := http.Get(strings.TrimPrefix(opened[1], "bridge "))
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(page.Body)
			page.Body.Close()
			if page.StatusCode != http.StatusOK || !strings.Contains(string(body), `"source":"/bridge/v1/media/clip"`) {
				t.Fatalf("clip page = %d %s", page.StatusCode, body)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("opened = %v", opened)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type mixedLauncher struct {
	capturingBrowserLauncher
	web *capturingBrowserSession
}

func (l *mixedLauncher) LaunchWeb(context.Context, WebLaunch) (BrowserSession, error) {
	return l.web, nil
}

// A desktop node shares one Chromium adapter for media and web, so the web
// candidate's probation must be judged on the pending web state, not on the
// committed media presentation it is replacing.
func TestChromiumWebProbationAfterCommittedMedia(t *testing.T) {
	mediaPath := t.TempDir() + "/image.png"
	if err := os.WriteFile(mediaPath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	launcher := &mixedLauncher{
		capturingBrowserLauncher: capturingBrowserLauncher{target: make(chan string, 1), session: &capturingBrowserSession{alive: true}},
		web:                      &capturingBrowserSession{alive: true},
	}
	adapter, err := NewChromium(ChromiumConfig{RuntimeRoot: t.TempDir(), Timeout: 2 * time.Second, Launcher: launcher})
	if err != nil {
		t.Fatal(err)
	}
	media := Candidate{Kind: release.PresentationMedia, ReleaseDigest: strings.Repeat("a", 64), Source: mediaPath, MediaType: "image/png", Fit: "fill", Volume: 64}
	if err := adapter.Prepare(context.Background(), media); err != nil {
		t.Fatal(err)
	}
	startDone := make(chan error, 1)
	go func() { startDone <- adapter.Start(context.Background(), media) }()
	target := <-launcher.target
	ready, err := http.Post(strings.TrimSuffix(target, "/")+"/bridge/v1/ready", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	ready.Body.Close()
	if err := <-startDone; err != nil {
		t.Fatal(err)
	}
	if err := adapter.Commit(context.Background(), media); err != nil {
		t.Fatal(err)
	}
	web := Candidate{Kind: release.PresentationWeb, ReleaseDigest: strings.Repeat("b", 64), WebURL: "https://example.com/"}
	if err := adapter.Prepare(context.Background(), web); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background(), web); err != nil {
		t.Fatal(err)
	}
	status, err := adapter.Health(context.Background(), web)
	if err != nil || !status.Healthy || status.ReleaseDigest != web.ReleaseDigest {
		t.Fatalf("web probation health = %#v, %v", status, err)
	}
}

func TestHTTPBrowserLauncherWebWindowAndInput(t *testing.T) {
	var created map[string]any
	var input struct {
		Events []playerclient.RemoteEvent `json:"events"`
	}
	var updated map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/windows":
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "w1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/windows/w1/input":
			_ = json.NewDecoder(r.Body).Decode(&input)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/windows/w1/remote":
			_ = json.NewDecoder(r.Body).Decode(&updated)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	launcher, err := NewHTTPBrowserLauncher(server.URL, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	session, err := launcher.LaunchWeb(context.Background(), WebLaunch{URL: "https://example.com/", Remote: &RemoteDescriptor{URL: "https://control.example/r", ShowQR: true}})
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := created["remote"].(map[string]any)
	if created["kind"] != "web" || created["url"] != "https://example.com/" || remote["url"] != "https://control.example/r" || remote["showQr"] != true {
		t.Fatalf("create body = %#v", created)
	}
	if err := session.(BrowserInputSession).SendInput(context.Background(), []playerclient.RemoteEvent{{Sequence: 7, Type: "wheel", DeltaY: 1}}); err != nil {
		t.Fatal(err)
	}
	if len(input.Events) != 1 || input.Events[0].Sequence != 7 {
		t.Fatalf("input = %#v", input)
	}
	if err := session.(BrowserRemoteSession).UpdateRemote(context.Background(), RemoteDescriptor{URL: "https://control.example/r2"}); err != nil {
		t.Fatal(err)
	}
	if updated["url"] != "https://control.example/r2" || updated["showQr"] != false {
		t.Fatalf("remote update = %#v", updated)
	}
}
