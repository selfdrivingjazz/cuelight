package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	"github.com/jmilldotdev/cuelight/internal/player/screenshot"
	"github.com/jmilldotdev/cuelight/internal/release"
)

type RemoteEventSource interface {
	PollRemoteEvents(context.Context, string, int64, time.Duration) (playerclient.RemoteEventPage, error)
}

// RemoteSessionCreator is implemented by remote sources that can replace a
// session the control plane no longer knows (expired or lost on restart).
type RemoteSessionCreator interface {
	CreateRemoteSession(context.Context, int64) (playerclient.RemoteSession, error)
}

// AppTokenSource mints app tokens for web presentations whose media item has
// app access.
type AppTokenSource interface {
	CreateAppToken(context.Context, string) (playerclient.AppToken, error)
}

type ChromiumConfig struct {
	BinaryPath   string
	RuntimeRoot  string
	LoopbackAddr string
	Timeout      time.Duration
	ExtraArgs    []string
	Output       io.Writer
	Launcher     BrowserLauncher
	Remote       RemoteEventSource
	AppTokens    AppTokenSource
}

// Chromium owns the local media bridge server and the browser process. It
// never forwards control-plane credentials to Chromium; bridge pages see only
// the loopback URL and app-access pages see only their scoped app token.
type Chromium struct {
	RuntimeRoot  string
	LoopbackAddr string
	Timeout      time.Duration
	Output       io.Writer
	launcher     BrowserLauncher
	Remote       RemoteEventSource
	AppTokens    AppTokenSource

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	session  BrowserSession
	// stopWorker cancels the goroutine bound to session: the web remote-input
	// forwarder or the media-schedule runner.
	stopWorker context.CancelFunc
	pending    *chromiumState
	current    *chromiumState
}

type chromiumState struct {
	candidate  Candidate
	root       string
	profile    string
	entrypoint string
	healthPath string
	media      map[string]string
	ready      bool

	// scheduleItem is the schedule item currently on screen ("" when the
	// schedule shows its blank page); guarded by Chromium.mu.
	scheduleItem string

	// remoteMu guards remote and the renewal backoff once the state is serving.
	remoteMu      sync.Mutex
	remote        *RemoteDescriptor
	remoteRetryAt time.Time
	remoteBackoff time.Duration
}

func NewChromium(config ChromiumConfig) (*Chromium, error) {
	if config.RuntimeRoot == "" {
		return nil, errors.New("Chromium runtime root is required")
	}
	if config.Launcher == nil {
		if config.BinaryPath == "" {
			return nil, errors.New("Chromium binary or browser launcher is required")
		}
		config.Launcher = ExecBrowserLauncher{BinaryPath: config.BinaryPath, ExtraArgs: config.ExtraArgs, Output: config.Output}
	}
	if config.LoopbackAddr == "" {
		config.LoopbackAddr = "127.0.0.1:0"
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	if host, _, err := net.SplitHostPort(config.LoopbackAddr); err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("Chromium bridge must bind a loopback IP address")
	}
	return &Chromium{RuntimeRoot: config.RuntimeRoot, LoopbackAddr: config.LoopbackAddr, Timeout: config.Timeout, Output: config.Output, Remote: config.Remote, AppTokens: config.AppTokens, launcher: config.Launcher}, nil
}

func (c *Chromium) Prepare(_ context.Context, candidate Candidate) error {
	switch candidate.Kind {
	case release.PresentationMedia:
		return c.prepareMedia(candidate)
	case release.PresentationMediaSchedule:
		return c.prepareSchedule(candidate)
	case release.PresentationWeb:
		return c.prepareWeb(candidate)
	default:
		return errors.New("Chromium requires a media, media-schedule, or web candidate")
	}
}

func (c *Chromium) prepareWeb(candidate Candidate) error {
	if candidate.ReleaseDigest == "" {
		return errors.New("web candidate is incomplete")
	}
	if err := release.ValidateWebURL(candidate.WebURL); err != nil {
		return err
	}
	if _, ok := c.launcher.(WebBrowserLauncher); !ok {
		return errors.New("web presentations require the desktop renderer")
	}
	c.mu.Lock()
	removeChromiumState(c.pending)
	c.pending = &chromiumState{candidate: candidate, remote: copyRemote(candidate.Remote)}
	c.mu.Unlock()
	return nil
}

// mediaPage is one bridge page showing a single image or video.
type mediaPage struct {
	MediaType   string
	Source      string
	Fit         string
	Loop        bool
	Muted       bool
	Volume      int
	Orientation int
}

func validateMediaPage(page mediaPage) error {
	if page.MediaType != "video/mp4" && page.MediaType != "image/jpeg" && page.MediaType != "image/png" && page.MediaType != "image/webp" {
		return fmt.Errorf("unsupported browser media type %q", page.MediaType)
	}
	if page.Fit != "fit" && page.Fit != "fill" {
		return errors.New("media fit must be fit or fill")
	}
	if page.Volume < 0 || page.Volume > 100 {
		return errors.New("media volume must be between 0 and 100")
	}
	return nil
}

// mediaDocument renders the bridge page for page; servedSource is the URL the
// page loads (a loopback stream URL or a bridge media path).
func mediaDocument(page mediaPage, servedSource string) ([]byte, error) {
	kind := "img"
	if page.MediaType == "video/mp4" {
		kind = "video"
	}
	config, err := json.Marshal(struct {
		Kind        string `json:"kind"`
		Source      string `json:"source"`
		Fit         string `json:"fit"`
		Loop        bool   `json:"loop"`
		Muted       bool   `json:"muted"`
		Volume      int    `json:"volume"`
		Orientation int    `json:"orientation"`
	}{kind, servedSource, page.Fit, page.Loop, page.Muted, page.Volume, page.Orientation})
	if err != nil {
		return nil, err
	}
	return []byte(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>html,body,#root{width:100%;height:100%;margin:0;background:#000;overflow:hidden}#root{display:grid;place-items:center}video,img{width:100%;height:100%;background:#000}</style></head><body><div id="root"></div><script>const c=` + string(config) + `;const m=document.createElement(c.kind);m.src=c.source;m.style.objectFit=c.fit==="fill"?"cover":"contain";m.style.transform="rotate("+c.orientation+"deg)";m.addEventListener(c.kind==="video"?"canplay":"load",()=>fetch("/bridge/v1/ready",{method:"POST"}),{once:true});m.addEventListener("error",()=>document.body.dataset.playerError="true");if(c.kind==="video"){m.autoplay=true;m.playsInline=true;m.loop=c.loop;m.muted=c.muted;m.volume=c.volume/100;m.play().catch(()=>{});}document.getElementById("root").appendChild(m);</script></body></html>`), nil
}

const blankDocument = `<!doctype html><html><head><meta charset="utf-8"><style>html,body{width:100%;height:100%;margin:0;background:#000}</style></head><body><script>fetch("/bridge/v1/ready",{method:"POST"})</script></body></html>`

// servedMediaSource maps a renderer source to what the bridge page loads:
// loopback stream URLs are used directly, cached files are served by key.
func servedMediaSource(source, key string, media map[string]string) (string, error) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return source, nil
	}
	info, err := os.Stat(source)
	if err != nil || info.IsDir() {
		return "", errors.New("cached media source is unavailable")
	}
	media[key] = source
	return "/bridge/v1/media/" + key, nil
}

func (c *Chromium) prepareMedia(candidate Candidate) error {
	if candidate.ReleaseDigest == "" || candidate.Source == "" {
		return errors.New("media candidate is incomplete")
	}
	page := mediaPage{MediaType: candidate.MediaType, Source: candidate.Source, Fit: candidate.Fit, Loop: candidate.Loop, Muted: candidate.Muted, Volume: candidate.Volume, Orientation: candidate.Orientation}
	if err := validateMediaPage(page); err != nil {
		return err
	}
	root, err := os.MkdirTemp(c.RuntimeRoot, "media-")
	if err != nil {
		return fmt.Errorf("create media runtime root: %w", err)
	}
	media := make(map[string]string)
	source, err := servedMediaSource(candidate.Source, "current", media)
	if err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	document, err := mediaDocument(page, source)
	if err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), document, 0o600); err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	profile := root + ".profile"
	if err := os.MkdirAll(profile, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	c.mu.Lock()
	removeChromiumState(c.pending)
	c.pending = &chromiumState{candidate: candidate, root: root, profile: profile, entrypoint: "index.html", healthPath: "/", media: media}
	c.mu.Unlock()
	return nil
}

// prepareSchedule writes one bridge page per media item; web items are shown
// as live pages through the desktop renderer for their scheduled duration.
func (c *Chromium) prepareSchedule(candidate Candidate) error {
	if candidate.ReleaseDigest == "" || len(candidate.Schedule) == 0 || !candidate.WindowStart.Before(candidate.WindowEnd) {
		return errors.New("media schedule candidate is incomplete")
	}
	root, err := os.MkdirTemp(c.RuntimeRoot, "schedule-")
	if err != nil {
		return fmt.Errorf("create schedule runtime root: %w", err)
	}
	fail := func(err error) error {
		_ = os.RemoveAll(root)
		return err
	}
	if err := os.Mkdir(filepath.Join(root, "items"), 0o700); err != nil {
		return fail(err)
	}
	media := make(map[string]string)
	for _, item := range candidate.Schedule {
		if item.ID == "" || strings.ContainsAny(item.ID, `/\`) || !item.StartsAt.Before(item.EndsAt) {
			return fail(errors.New("schedule item id or window is invalid"))
		}
		if item.WebURL != "" {
			if err := release.ValidateWebURL(item.WebURL); err != nil {
				return fail(fmt.Errorf("schedule item %q: %w", item.ID, err))
			}
			if _, ok := c.launcher.(WebBrowserLauncher); !ok {
				return fail(errors.New("web schedule items require the desktop renderer"))
			}
			continue
		}
		page := mediaPage{MediaType: item.MediaType, Source: item.Source, Fit: item.Fit, Loop: item.Loop, Muted: item.Muted, Volume: item.Volume, Orientation: item.Orientation}
		if item.Source == "" {
			return fail(fmt.Errorf("schedule item %q has no source", item.ID))
		}
		if err := validateMediaPage(page); err != nil {
			return fail(fmt.Errorf("schedule item %q: %w", item.ID, err))
		}
		source, err := servedMediaSource(item.Source, item.ID, media)
		if err != nil {
			return fail(fmt.Errorf("schedule item %q: %w", item.ID, err))
		}
		document, err := mediaDocument(page, source)
		if err != nil {
			return fail(err)
		}
		if err := os.WriteFile(filepath.Join(root, "items", item.ID+".html"), document, 0o600); err != nil {
			return fail(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "blank.html"), []byte(blankDocument), 0o600); err != nil {
		return fail(err)
	}
	profile := root + ".profile"
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return fail(err)
	}
	c.mu.Lock()
	removeChromiumState(c.pending)
	c.pending = &chromiumState{candidate: candidate, root: root, profile: profile, entrypoint: "blank.html", healthPath: "/blank.html", media: media}
	c.mu.Unlock()
	return nil
}

func (c *Chromium) Start(ctx context.Context, candidate Candidate) error {
	c.mu.Lock()
	state := c.pending
	if state == nil || state.candidate.ReleaseDigest != candidate.ReleaseDigest {
		c.mu.Unlock()
		return errors.New("Chromium candidate was not prepared")
	}
	if c.server != nil || c.session != nil {
		oldServer, oldSession, oldWorker := c.server, c.session, c.stopWorker
		c.server, c.listener, c.session, c.stopWorker = nil, nil, nil, nil
		c.mu.Unlock()
		if oldWorker != nil {
			oldWorker()
		}
		if oldSession != nil {
			_ = oldSession.Stop(context.Background())
		}
		if oldServer != nil {
			_ = oldServer.Shutdown(context.Background())
		}
		c.mu.Lock()
	}
	if state.candidate.Kind == release.PresentationWeb {
		c.mu.Unlock()
		return c.startWeb(ctx, state)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/bridge/v1/ready", func(w http.ResponseWriter, r *http.Request) { c.serveReady(w, r, state) })
	mux.HandleFunc("/bridge/v1/media/", func(w http.ResponseWriter, r *http.Request) { c.serveMedia(w, r, state) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { c.serveStatic(w, r, state) })
	listener, err := net.Listen("tcp", c.LoopbackAddr)
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("listen Chromium bridge: %w", err)
	}
	handler := http.Handler(mux)
	if c.Output != nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(c.Output, "Chromium bridge %s %s\n", r.Method, r.URL.Path)
			mux.ServeHTTP(w, r)
		})
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	c.listener, c.server = listener, server
	c.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
	base := "http://" + listener.Addr().String()
	if state.candidate.Kind == release.PresentationMediaSchedule {
		return c.startSchedule(ctx, state, base)
	}
	session, err := c.launcher.Launch(ctx, base+"/", state.profile)
	if err != nil {
		_ = server.Shutdown(context.Background())
		c.mu.Lock()
		c.server, c.listener = nil, nil
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.session = session
	c.mu.Unlock()
	if err := c.waitReady(ctx); err != nil {
		_ = c.abortPending(context.Background())
		return err
	}
	return nil
}

// startSchedule shows the item active now, waits for the bridge page (or the
// web window) to be ready, then hands item boundaries to runSchedule.
func (c *Chromium) startSchedule(ctx context.Context, state *chromiumState, base string) error {
	now := time.Now()
	item, active := SelectScheduleItem(state.candidate.Schedule, now)
	var selected *ScheduleItem
	if active {
		selected = &item
	}
	if err := c.showScheduleItem(ctx, state, base, selected); err != nil {
		_ = c.abortPending(context.Background())
		return err
	}
	if selected == nil || selected.WebURL != "" {
		c.mu.Lock()
		state.ready = true
		c.mu.Unlock()
	} else if err := c.waitReady(ctx); err != nil {
		_ = c.abortPending(context.Background())
		return err
	}
	workerCtx, stop := context.WithCancel(context.Background())
	c.mu.Lock()
	c.stopWorker = stop
	c.mu.Unlock()
	go c.runSchedule(workerCtx, state, base)
	return nil
}

func (c *Chromium) runSchedule(ctx context.Context, state *chromiumState, base string) {
	for {
		now := time.Now()
		next := nextScheduleBoundary(state.candidate, now)
		if next.IsZero() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		item, active := SelectScheduleItem(state.candidate.Schedule, time.Now())
		var selected *ScheduleItem
		if active {
			selected = &item
		}
		if err := c.showScheduleItem(ctx, state, base, selected); err != nil && ctx.Err() == nil {
			c.logf("media schedule transition failed: %v\n", err)
		}
	}
}

// nextScheduleBoundary is the first item start or end after now, or zero once
// the schedule window has ended.
func nextScheduleBoundary(candidate Candidate, now time.Time) time.Time {
	var next time.Time
	consider := func(at time.Time) {
		if at.After(now) && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	consider(candidate.WindowStart)
	consider(candidate.WindowEnd)
	for _, item := range candidate.Schedule {
		consider(item.StartsAt)
		consider(item.EndsAt)
	}
	return next
}

// showScheduleItem replaces the on-screen window with item (nil = blank).
func (c *Chromium) showScheduleItem(ctx context.Context, state *chromiumState, base string, item *ScheduleItem) error {
	itemID := ""
	if item != nil {
		itemID = item.ID
	}
	c.mu.Lock()
	if c.session != nil && state.scheduleItem == itemID {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	var (
		session BrowserSession
		err     error
	)
	switch {
	case item == nil:
		session, err = c.launcher.Launch(ctx, base+"/blank.html", state.profile)
	case item.WebURL != "":
		launcher, ok := c.launcher.(WebBrowserLauncher)
		if !ok {
			return errors.New("web schedule items require the desktop renderer")
		}
		session, err = launcher.LaunchWeb(ctx, WebLaunch{URL: item.WebURL})
	default:
		session, err = c.launcher.Launch(ctx, base+"/items/"+url.PathEscape(item.ID)+".html", state.profile)
	}
	if err != nil {
		return err
	}
	c.mu.Lock()
	if ctx.Err() != nil {
		// Stopped or replaced while launching: never resurrect a window.
		c.mu.Unlock()
		_ = session.Stop(context.Background())
		return ctx.Err()
	}
	previous := c.session
	c.session, state.scheduleItem = session, itemID
	c.mu.Unlock()
	if previous != nil && previous != session {
		_ = previous.Stop(context.Background())
	}
	return nil
}

// startWeb opens the website directly in the desktop renderer. Readiness is a
// successful launch of a live window. Plain sites receive remote input as
// injected native events; app-access pages are loaded with a scoped app token
// in their URL fragment and consume the remote session themselves.
func (c *Chromium) startWeb(ctx context.Context, state *chromiumState) error {
	launcher, ok := c.launcher.(WebBrowserLauncher)
	if !ok {
		return errors.New("web presentations require the desktop renderer")
	}
	launch := WebLaunch{URL: state.candidate.WebURL, Remote: state.candidate.Remote}
	app := state.candidate.AppAccess
	if app {
		if c.AppTokens == nil {
			return errors.New("app-access web presentations require an app token source")
		}
		token, err := c.AppTokens.CreateAppToken(ctx, state.candidate.MediaID)
		if err != nil {
			return fmt.Errorf("mint app token: %w", err)
		}
		launch.URL, launch.Remote = WithAppToken(launch.URL, token.Token, token.APIURL), nil
	}
	session, err := launcher.LaunchWeb(ctx, launch)
	if err != nil {
		return err
	}
	alive, err := session.Alive(ctx)
	if err == nil && !alive {
		err = errors.New("web window exited during startup")
	}
	if err != nil {
		_ = session.Stop(context.Background())
		return err
	}
	var stopWorker context.CancelFunc
	if state.remote != nil && c.Remote != nil {
		var workerCtx context.Context
		if input, ok := session.(BrowserInputSession); ok && !app {
			workerCtx, stopWorker = context.WithCancel(context.Background())
			go c.forwardRemoteInput(workerCtx, state, input)
		} else if app {
			workerCtx, stopWorker = context.WithCancel(context.Background())
			go c.keepRemoteSession(workerCtx, state)
		}
	}
	c.mu.Lock()
	c.session, c.stopWorker = session, stopWorker
	state.ready = true
	c.mu.Unlock()
	return nil
}

// keepRemoteSession replaces the remote session when it expires so an
// app-access page, which polls the node's active session itself, keeps a live
// remote.
func (c *Chromium) keepRemoteSession(ctx context.Context, state *chromiumState) {
	for {
		current := state.currentRemote()
		if current.ExpiresAt.IsZero() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(current.ExpiresAt)):
		}
		if _, err := c.renewRemote(ctx, state, current.SessionID); err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logf("app remote session renewal failed: %v\n", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

// WithAppToken appends the app token and API base to pageURL's fragment as
// cuelight-token and cuelight-api, keeping other fragment parameters and
// replacing stale cuelight ones.
func WithAppToken(pageURL, token, apiURL string) string {
	base, fragment, _ := strings.Cut(pageURL, "#")
	parts := []string{}
	for _, part := range strings.Split(fragment, "&") {
		name, _, _ := strings.Cut(part, "=")
		if part == "" || name == "cuelight-token" || name == "cuelight-api" {
			continue
		}
		parts = append(parts, part)
	}
	parts = append(parts, "cuelight-token="+url.QueryEscape(token), "cuelight-api="+url.QueryEscape(apiURL))
	return base + "#" + strings.Join(parts, "&")
}

const remoteInputPollWait = 25 * time.Second

func (c *Chromium) forwardRemoteInput(ctx context.Context, state *chromiumState, input BrowserInputSession) {
	var after int64
	for ctx.Err() == nil {
		current := state.currentRemote()
		page, err := c.Remote.PollRemoteEvents(ctx, current.SessionID, after, remoteInputPollWait)
		if errors.Is(err, playerclient.ErrRemoteSessionGone) && ctx.Err() == nil {
			renewed, renewErr := c.renewRemote(ctx, state, current.SessionID)
			if renewErr == nil {
				if updater, ok := input.(BrowserRemoteSession); ok {
					if err := updater.UpdateRemote(ctx, renewed); err != nil && ctx.Err() == nil {
						c.logf("remote QR refresh failed: %v\n", err)
					}
				}
				continue
			}
			err = renewErr
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logf("remote input poll failed: %v\n", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if len(page.Events) > 0 {
			if err := input.SendInput(ctx, page.Events); err != nil && ctx.Err() == nil {
				c.logf("remote input delivery failed: %v\n", err)
			}
		}
		if page.NextSequence > after {
			after = page.NextSequence
		}
	}
}

const (
	remoteRenewMinBackoff = time.Second
	remoteRenewMaxBackoff = 30 * time.Second
)

var errRemoteRenewBackoff = errors.New("remote session renewal backing off")

func copyRemote(remote *RemoteDescriptor) *RemoteDescriptor {
	if remote == nil || remote.SessionID == "" {
		return nil
	}
	value := *remote
	return &value
}

func (s *chromiumState) currentRemote() RemoteDescriptor {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	return *s.remote
}

// renewRemote replaces the remote session staleID once: concurrent pollers
// serialise on remoteMu and reuse the replacement, and failures back off
// exponentially so a down control plane is not stampeded.
func (c *Chromium) renewRemote(ctx context.Context, state *chromiumState, staleID string) (RemoteDescriptor, error) {
	state.remoteMu.Lock()
	defer state.remoteMu.Unlock()
	if state.remote.SessionID != staleID {
		return *state.remote, nil
	}
	creator, ok := c.Remote.(RemoteSessionCreator)
	if !ok {
		return RemoteDescriptor{}, errors.New("remote source cannot renew sessions")
	}
	now := time.Now()
	if now.Before(state.remoteRetryAt) {
		return RemoteDescriptor{}, errRemoteRenewBackoff
	}
	session, err := creator.CreateRemoteSession(ctx, state.remote.Generation)
	if err == nil && session.ID == "" {
		err = errors.New("renewed remote session has no id")
	}
	if err != nil {
		if ctx.Err() == nil {
			state.remoteBackoff = min(max(2*state.remoteBackoff, remoteRenewMinBackoff), remoteRenewMaxBackoff)
			state.remoteRetryAt = now.Add(state.remoteBackoff)
		}
		return RemoteDescriptor{}, fmt.Errorf("renew remote session: %w", err)
	}
	state.remoteBackoff, state.remoteRetryAt = 0, time.Time{}
	state.remote = &RemoteDescriptor{SessionID: session.ID, Generation: state.remote.Generation, URL: session.RemoteURL, ShowQR: session.ShowQR, ExpiresAt: session.ExpiresAt}
	c.logf("remote session %s renewed as %s\n", staleID, session.ID)
	return *state.remote, nil
}

func (c *Chromium) logf(format string, args ...any) {
	if c.Output != nil {
		_, _ = fmt.Fprintf(c.Output, format, args...)
	}
}

func (c *Chromium) waitReady(ctx context.Context) error {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		c.mu.Lock()
		listener, session := c.listener, c.session
		ready := c.pending != nil && c.pending.ready
		c.mu.Unlock()
		if listener == nil {
			return errors.New("Chromium bridge stopped during startup")
		}
		if ready {
			return nil
		}
		if session != nil {
			alive, err := session.Alive(checkCtx)
			if err != nil {
				return err
			}
			if !alive {
				return errors.New("Chromium exited before bridge readiness")
			}
		}
		select {
		case <-checkCtx.Done():
			return fmt.Errorf("Chromium bridge readiness: %w", checkCtx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (c *Chromium) abortPending(ctx context.Context) error {
	c.mu.Lock()
	server, session, pending, stopWorker := c.server, c.session, c.pending, c.stopWorker
	c.server, c.listener, c.session, c.pending, c.stopWorker = nil, nil, nil, nil, nil
	c.mu.Unlock()
	if stopWorker != nil {
		stopWorker()
	}
	if session != nil {
		_ = session.Stop(ctx)
	}
	var result error
	if server != nil {
		result = errors.Join(result, server.Shutdown(ctx))
	}
	removeChromiumState(pending)
	return result
}

func (c *Chromium) Commit(_ context.Context, candidate Candidate) error {
	c.mu.Lock()
	if c.pending == nil || c.pending.candidate.ReleaseDigest != candidate.ReleaseDigest {
		c.mu.Unlock()
		return errors.New("candidate is not the prepared Chromium state")
	}
	previous := c.current
	c.current, c.pending = c.pending, nil
	c.mu.Unlock()
	removeChromiumState(previous)
	return nil
}

func (c *Chromium) Stop(ctx context.Context) error {
	c.mu.Lock()
	server, session, current, pending, stopWorker := c.server, c.session, c.current, c.pending, c.stopWorker
	c.server, c.listener, c.session, c.current, c.pending, c.stopWorker = nil, nil, nil, nil, nil, nil
	c.mu.Unlock()
	if stopWorker != nil {
		stopWorker()
	}
	var result error
	if session != nil {
		result = errors.Join(result, session.Stop(ctx))
	}
	if server != nil {
		result = errors.Join(result, server.Shutdown(ctx))
	}
	removeChromiumState(current)
	removeChromiumState(pending)
	return result
}

func removeChromiumState(state *chromiumState) {
	if state == nil || state.root == "" {
		return
	}
	_ = os.RemoveAll(state.root)
	_ = os.RemoveAll(state.profile)
}

func (c *Chromium) Current(ctx context.Context) (Status, error) {
	c.mu.Lock()
	current := c.current
	c.mu.Unlock()
	if current == nil {
		return Status{}, nil
	}
	return c.Health(ctx, current.candidate)
}

func (c *Chromium) Capture(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	session := c.session
	current := c.current
	c.mu.Unlock()
	if session == nil || current == nil {
		return nil, screenshot.ErrUnsupported
	}
	capture, ok := session.(BrowserScreenshotSession)
	if !ok {
		return nil, screenshot.ErrUnsupported
	}
	data, err := capture.Screenshot(ctx)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) == 0 {
		return nil, screenshot.ErrInvalidPNG
	}
	return data, nil
}

func (c *Chromium) serveMedia(w http.ResponseWriter, r *http.Request, state *chromiumState) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bridge/v1/media/")
	path, ok := state.media[key]
	if !ok || strings.Contains(key, "/") {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, key, info.ModTime(), file)
}
func (c *Chromium) serveStatic(w http.ResponseWriter, r *http.Request, state *chromiumState) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, "/")
	if relative == "" {
		relative = state.entrypoint
	}
	path, err := safeJoin(state.root, relative)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, filepath.Base(relative), info.ModTime(), file)
}
func (c *Chromium) Health(ctx context.Context, candidate Candidate) (Status, error) {
	c.mu.Lock()
	state := c.current
	if c.pending != nil && (state == nil || c.pending.candidate.ReleaseDigest == candidate.ReleaseDigest) {
		state = c.pending
	}
	listener, session := c.listener, c.session
	ready := state != nil && state.ready
	c.mu.Unlock()
	if state != nil && state.candidate.Kind == release.PresentationWeb {
		return webHealth(ctx, candidate, session, ready)
	}
	if state == nil || listener == nil || !ready {
		return Status{ReleaseDigest: candidate.ReleaseDigest, Candidate: candidate, Current: staleContent(candidate)}, nil
	}
	if session != nil {
		alive, err := session.Alive(ctx)
		if err != nil {
			return Status{}, err
		}
		if !alive {
			return Status{ReleaseDigest: candidate.ReleaseDigest, Path: listener.Addr().String(), Candidate: candidate, Current: staleContent(candidate)}, nil
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+listener.Addr().String()+state.healthPath, nil)
	if err != nil {
		return Status{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	healthy := response.StatusCode == http.StatusOK
	current := observedContent(candidate, healthy, time.Now())
	return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: healthy, Path: listener.Addr().String(), Candidate: candidate, Current: current}, nil
}

func webHealth(ctx context.Context, candidate Candidate, session BrowserSession, ready bool) (Status, error) {
	if session == nil || !ready {
		return Status{ReleaseDigest: candidate.ReleaseDigest, Candidate: candidate, Current: staleContent(candidate)}, nil
	}
	alive, err := session.Alive(ctx)
	if err != nil {
		return Status{}, err
	}
	return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: alive, Candidate: candidate, Current: observedContent(candidate, alive, time.Now())}, nil
}

func staleContent(candidate Candidate) *playerclient.CurrentContent {
	now := time.Now().UTC()
	return &playerclient.CurrentContent{ReleaseDigest: candidate.ReleaseDigest, PresentationKind: candidate.Kind, State: playerclient.ContentStateStale, TransitionedAt: now}
}

func observedContent(candidate Candidate, healthy bool, now time.Time) *playerclient.CurrentContent {
	now = now.UTC()
	content := &playerclient.CurrentContent{ReleaseDigest: candidate.ReleaseDigest, PresentationKind: candidate.Kind, State: playerclient.ContentStatePlaying, TransitionedAt: now}
	if !healthy {
		content.State = playerclient.ContentStateStale
		return content
	}
	if candidate.Kind == release.PresentationMediaSchedule {
		item, ok := SelectScheduleItem(candidate.Schedule, now)
		if !ok {
			content.State = playerclient.ContentStateOffAir
			return content
		}
		start := item.StartsAt.UTC()
		content.MediaID, content.AssetID = item.MediaID, item.AssetID
		content.StartedAt, content.TransitionedAt = &start, start
		return content
	}
	start := now
	content.StartedAt = &start
	if candidate.MediaID != "" {
		content.MediaID = candidate.MediaID
	}
	content.AssetID = candidate.AssetID
	return content
}

func (c *Chromium) serveReady(w http.ResponseWriter, r *http.Request, state *chromiumState) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.mu.Lock()
	state.ready = true
	c.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func safeJoin(root, name string) (string, error) {
	if name == "" || strings.HasPrefix(filepath.ToSlash(name), "/") || filepath.IsAbs(name) {
		return "", errors.New("absolute path")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal")
	}
	result, err := filepath.Abs(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	base, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if result != base && !strings.HasPrefix(result, base+string(filepath.Separator)) {
		return "", errors.New("path escapes root")
	}
	parts := strings.Split(strings.TrimPrefix(result, base+string(filepath.Separator)), string(filepath.Separator))
	cursor := base
	for _, part := range parts[:max(0, len(parts)-1)] {
		cursor = filepath.Join(cursor, part)
		info, err := os.Lstat(cursor)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("path traverses symlink")
		}
	}
	return result, nil
}

var _ Adapter = (*Chromium)(nil)
