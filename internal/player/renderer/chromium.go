package renderer

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jmill/cable/internal/release"
)

type ChromiumConfig struct {
	BinaryPath   string
	RuntimeRoot  string
	LoopbackAddr string
	Timeout      time.Duration
	ExtraArgs    []string
	Output       io.Writer
	Launcher     BrowserLauncher
}

// Chromium owns the local shell server and the browser process. It never
// forwards control-plane credentials to Chromium; the browser sees only the
// loopback bridge URL.
type Chromium struct {
	RuntimeRoot  string
	LoopbackAddr string
	Timeout      time.Duration
	Output       io.Writer
	launcher     BrowserLauncher

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	session  BrowserSession
	pending  *chromiumState
	current  *chromiumState
}

type chromiumState struct {
	candidate    Candidate
	root         string
	profile      string
	entrypoint   string
	healthPath   string
	presentation []byte
	media        map[string]string
	ready        bool
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
	return &Chromium{RuntimeRoot: config.RuntimeRoot, LoopbackAddr: config.LoopbackAddr, Timeout: config.Timeout, Output: config.Output, launcher: config.Launcher}, nil
}

func (c *Chromium) Prepare(_ context.Context, candidate Candidate) error {
	if candidate.Kind == release.PresentationMedia {
		return c.prepareMedia(candidate)
	}
	if candidate.Kind != release.PresentationCable {
		return errors.New("Chromium requires a media or Cable candidate")
	}
	if candidate.ReleaseDigest == "" || candidate.ShellPath == "" || candidate.RevisionPath == "" || candidate.Entrypoint == "" || candidate.BridgeProtocol == "" {
		return errors.New("cable candidate is incomplete")
	}
	if err := release.ValidateDigest(candidate.ShellBuildDigest); err != nil {
		return fmt.Errorf("shell build digest: %w", err)
	}
	if err := release.ValidateDigest(candidate.RevisionArtifactDigest); err != nil {
		return fmt.Errorf("revision artifact digest: %w", err)
	}
	rawRevision, err := os.ReadFile(candidate.RevisionPath)
	if err != nil {
		return fmt.Errorf("read revision artifact: %w", err)
	}
	if err := verifyBytesDigest(rawRevision, candidate.RevisionArtifactDigest); err != nil {
		return fmt.Errorf("verify revision artifact: %w", err)
	}
	if err := validateRevision(rawRevision, candidate); err != nil {
		return err
	}
	presentation, err := transformRevision(rawRevision, candidate)
	if err != nil {
		return err
	}
	root, err := os.MkdirTemp(c.RuntimeRoot, "cable-")
	if err != nil {
		return fmt.Errorf("create Chromium runtime root: %w", err)
	}
	if err := extractArchive(candidate.ShellPath, root); err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	media := make(map[string]string)
	for digest, path := range candidate.ArtifactPaths {
		if !contains(candidate.ArtifactClosure, digest) || digest == candidate.ShellBuildDigest || digest == candidate.RevisionArtifactDigest {
			continue
		}
		if err := release.ValidateDigest(digest); err != nil {
			_ = os.RemoveAll(root)
			return err
		}
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			_ = os.RemoveAll(root)
			return fmt.Errorf("media artifact %s is unavailable", digest)
		}
		if err := verifyFileDigest(path, digest); err != nil {
			_ = os.RemoveAll(root)
			return fmt.Errorf("verify media artifact %s: %w", digest, err)
		}
		media[digest] = path
	}
	profile := root + ".profile"
	if err := os.MkdirAll(profile, 0o700); err != nil {
		_ = os.RemoveAll(root)
		return fmt.Errorf("create Chromium profile: %w", err)
	}
	c.mu.Lock()
	removeChromiumState(c.pending)
	c.pending = &chromiumState{candidate: candidate, root: root, profile: profile, entrypoint: candidate.Entrypoint, healthPath: "/bridge/v1/presentation", presentation: presentation, media: media}
	c.mu.Unlock()
	return nil
}

func (c *Chromium) prepareMedia(candidate Candidate) error {
	if candidate.ReleaseDigest == "" || candidate.Source == "" {
		return errors.New("media candidate is incomplete")
	}
	if candidate.MediaType != "video/mp4" && candidate.MediaType != "image/jpeg" && candidate.MediaType != "image/png" && candidate.MediaType != "image/webp" {
		return fmt.Errorf("unsupported browser media type %q", candidate.MediaType)
	}
	if candidate.Fit != "fit" && candidate.Fit != "fill" {
		return errors.New("media fit must be fit or fill")
	}
	if candidate.Volume < 0 || candidate.Volume > 100 {
		return errors.New("media volume must be between 0 and 100")
	}
	root, err := os.MkdirTemp(c.RuntimeRoot, "media-")
	if err != nil {
		return fmt.Errorf("create media runtime root: %w", err)
	}
	source := candidate.Source
	media := make(map[string]string)
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		info, statErr := os.Stat(source)
		if statErr != nil || info.IsDir() {
			_ = os.RemoveAll(root)
			return errors.New("cached media source is unavailable")
		}
		media["current"] = source
		source = "/bridge/v1/media/current"
	}
	kind := "img"
	if candidate.MediaType == "video/mp4" {
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
	}{kind, source, candidate.Fit, candidate.Loop, candidate.Muted, candidate.Volume, candidate.Orientation})
	if err != nil {
		_ = os.RemoveAll(root)
		return err
	}
	document := `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><style>html,body,#root{width:100%;height:100%;margin:0;background:#000;overflow:hidden}#root{display:grid;place-items:center}video,img{width:100%;height:100%;background:#000}</style></head><body><div id="root"></div><script>const c=` + string(config) + `;const m=document.createElement(c.kind);m.src=c.source;m.style.objectFit=c.fit==="fill"?"cover":"contain";m.style.transform="rotate("+c.orientation+"deg)";m.addEventListener(c.kind==="video"?"canplay":"load",()=>fetch("/bridge/v1/ready",{method:"POST"}),{once:true});m.addEventListener("error",()=>document.body.dataset.playerError="true");if(c.kind==="video"){m.autoplay=true;m.playsInline=true;m.loop=c.loop;m.muted=c.muted;m.volume=c.volume/100;m.play().catch(()=>{});}document.getElementById("root").appendChild(m);</script></body></html>`
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(document), 0o600); err != nil {
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

func (c *Chromium) Start(ctx context.Context, candidate Candidate) error {
	c.mu.Lock()
	state := c.pending
	if state == nil || state.candidate.ReleaseDigest != candidate.ReleaseDigest {
		c.mu.Unlock()
		return errors.New("cable candidate was not prepared")
	}
	if c.server != nil {
		oldServer, oldSession := c.server, c.session
		c.server, c.listener, c.session = nil, nil, nil
		c.mu.Unlock()
		if oldSession != nil {
			_ = oldSession.Stop(context.Background())
		}
		_ = oldServer.Shutdown(context.Background())
		c.mu.Lock()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/bridge/v1/presentation", func(w http.ResponseWriter, r *http.Request) { c.servePresentation(w, r, state) })
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
	entryURL := base + "/"
	session, err := c.launcher.Launch(ctx, entryURL, state.profile)
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
	server, session, pending := c.server, c.session, c.pending
	c.server, c.listener, c.session, c.pending = nil, nil, nil, nil
	c.mu.Unlock()
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
	server, session, current, pending := c.server, c.session, c.current, c.pending
	c.server, c.listener, c.session, c.current, c.pending = nil, nil, nil, nil, nil
	c.mu.Unlock()
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
	if state == nil {
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

func (c *Chromium) servePresentation(w http.ResponseWriter, r *http.Request, state *chromiumState) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(state.presentation)
	}
}
func (c *Chromium) serveMedia(w http.ResponseWriter, r *http.Request, state *chromiumState) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	digest := strings.TrimPrefix(r.URL.Path, "/bridge/v1/media/")
	path, ok := state.media[digest]
	if !ok || strings.Contains(digest, "/") {
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
	http.ServeContent(w, r, digest, info.ModTime(), file)
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
	if state == nil {
		state = c.pending
	}
	listener, session := c.listener, c.session
	ready := state != nil && state.ready
	c.mu.Unlock()
	if state == nil || listener == nil || !ready {
		return Status{ReleaseDigest: candidate.ReleaseDigest, Candidate: candidate}, nil
	}
	if session != nil {
		alive, err := session.Alive(ctx)
		if err != nil {
			return Status{}, err
		}
		if !alive {
			return Status{ReleaseDigest: candidate.ReleaseDigest, Path: listener.Addr().String(), Candidate: candidate}, nil
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
	return Status{ReleaseDigest: candidate.ReleaseDigest, Healthy: response.StatusCode == http.StatusOK, Path: listener.Addr().String(), Candidate: candidate}, nil
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
func extractArchive(path, destination string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open shell archive: %w", err)
	}
	defer file.Close()
	var input io.Reader = file
	magic := make([]byte, 2)
	if _, err := io.ReadFull(file, magic); err != nil {
		return fmt.Errorf("read shell archive: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return fmt.Errorf("open shell gzip: %w", err)
		}
		defer gz.Close()
		input = gz
	}
	reader := tar.NewReader(input)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read shell archive: %w", err)
		}
		name := filepath.ToSlash(header.Name)
		if header.Name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(header.Name) {
			return errors.New("shell archive contains an absolute path")
		}
		if _, err := safeJoin(destination, name); err != nil {
			return fmt.Errorf("unsafe shell archive path %q: %w", header.Name, err)
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			return fmt.Errorf("shell archive contains link %q", header.Name)
		}
		target, _ := safeJoin(destination, name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, io.LimitReader(reader, header.Size))
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported shell archive entry %q", header.Name)
		}
	}
	return nil
}

type revisionDocument struct {
	Schema            string    `json:"schema"`
	ID                string    `json:"id"`
	Digest            string    `json:"digest"`
	DeploymentID      string    `json:"deploymentId"`
	DeploymentVersion int64     `json:"deploymentVersion"`
	ShellBuildID      string    `json:"shellBuildId"`
	ShellBuildDigest  string    `json:"shellBuildDigest"`
	Entrypoint        string    `json:"entrypoint"`
	BridgeProtocol    string    `json:"bridgeProtocol"`
	GeneratedAt       time.Time `json:"generatedAt"`
	Theme             string    `json:"theme"`
	Timezone          string    `json:"timezone"`
	Window            struct {
		StartsAt time.Time `json:"startsAt"`
		EndsAt   time.Time `json:"endsAt"`
	} `json:"window"`
	Channels        []revisionChannel `json:"channels"`
	ArtifactDigests []string          `json:"artifactDigests"`
}
type revisionChannel struct {
	ID       string            `json:"id"`
	Number   int               `json:"number"`
	Name     string            `json:"name"`
	Callsign string            `json:"callsign"`
	Accent   string            `json:"accent"`
	Enabled  bool              `json:"enabled"`
	Standby  bool              `json:"standby"`
	Programs []revisionProgram `json:"programs"`
}
type revisionProgram struct {
	ID             string    `json:"id"`
	StartsAt       time.Time `json:"startsAt"`
	EndsAt         time.Time `json:"endsAt"`
	ArtifactDigest string    `json:"artifactDigest"`
	MediaType      string    `json:"mediaType"`
	Title          string    `json:"title"`
	Subtitle       string    `json:"subtitle"`
	PlacardPolicy  string    `json:"placardPolicy"`
	Fit            string    `json:"fit"`
	Loop           bool      `json:"loop"`
	Mute           bool      `json:"mute"`
	Volume         float64   `json:"volume"`
}

func transformRevision(data []byte, candidate Candidate) ([]byte, error) {
	var doc revisionDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode revision artifact: %w", err)
	}
	if doc.Schema != "cable.revision.v1" {
		return nil, errors.New("unsupported cable revision schema")
	}
	if doc.Theme == "" {
		doc.Theme = "nebula"
	}
	if doc.Theme != "nebula" && doc.Theme != "gallery" && doc.Theme != "noir" {
		return nil, errors.New("unsupported cable theme")
	}
	id := doc.ID
	if id == "" {
		id = candidate.RevisionArtifactDigest
	}
	digest := doc.Digest
	if digest == "" {
		digest = candidate.RevisionArtifactDigest
	}
	type revisionMeta struct {
		ID          string    `json:"id"`
		Digest      string    `json:"digest"`
		GeneratedAt time.Time `json:"generatedAt"`
		Timezone    string    `json:"timezone"`
		Stale       bool      `json:"stale"`
	}
	type outChannel struct {
		ID             string `json:"id"`
		Number         int    `json:"number"`
		Name           string `json:"name"`
		Callsign       string `json:"callsign"`
		Accent         string `json:"accent"`
		Enabled        bool   `json:"enabled"`
		StandbyMessage string `json:"standbyMessage,omitempty"`
	}
	type media struct {
		Kind      string `json:"kind"`
		Reference string `json:"reference"`
		Mime      string `json:"mime"`
	}
	type occurrence struct {
		ID        string    `json:"id"`
		ChannelID string    `json:"channelId"`
		StartsAt  time.Time `json:"startsAt"`
		EndsAt    time.Time `json:"endsAt"`
		Title     string    `json:"title"`
		Subtitle  string    `json:"subtitle,omitempty"`
		Media     media     `json:"media"`
		Placard   string    `json:"placard"`
		Fit       string    `json:"fit"`
		Loop      bool      `json:"loop"`
		Muted     bool      `json:"muted"`
		Volume    float64   `json:"volume"`
	}
	type bridge struct {
		ProtocolVersion string `json:"protocolVersion"`
		State           string `json:"state"`
	}
	type output struct {
		Revision    revisionMeta `json:"revision"`
		Channels    []outChannel `json:"channels"`
		Occurrences []occurrence `json:"occurrences"`
		Bridge      bridge       `json:"bridge"`
		Theme       string       `json:"theme"`
	}
	result := output{Revision: revisionMeta{ID: id, Digest: digest, GeneratedAt: doc.GeneratedAt, Timezone: doc.Timezone}, Channels: []outChannel{}, Occurrences: []occurrence{}, Theme: doc.Theme, Bridge: bridge{ProtocolVersion: candidate.BridgeProtocol, State: "online"}}
	for _, ch := range doc.Channels {
		oc := outChannel{ID: ch.ID, Number: ch.Number, Name: ch.Name, Callsign: ch.Callsign, Accent: ch.Accent, Enabled: ch.Enabled}
		if ch.Standby {
			oc.StandbyMessage = "Standby"
		}
		result.Channels = append(result.Channels, oc)
		for _, p := range ch.Programs {
			if !contains(candidate.ArtifactClosure, p.ArtifactDigest) {
				return nil, fmt.Errorf("revision references artifact outside closure: %s", p.ArtifactDigest)
			}
			kind, mime := "video", p.MediaType
			if strings.HasPrefix(p.MediaType, "image/") {
				kind = "image"
			}
			if (kind == "video" && mime != "video/mp4") || (kind == "image" && mime != "image/jpeg" && mime != "image/png" && mime != "image/webp") {
				return nil, fmt.Errorf("unsupported revision media type %q", mime)
			}
			placard := p.PlacardPolicy
			if placard == "" || placard == "default" {
				placard = "start"
			}
			if placard != "always" && placard != "start" && placard != "never" {
				return nil, fmt.Errorf("unsupported placard policy %q", placard)
			}
			result.Occurrences = append(result.Occurrences, occurrence{ID: p.ID, ChannelID: ch.ID, StartsAt: p.StartsAt, EndsAt: p.EndsAt, Title: p.Title, Subtitle: p.Subtitle, Media: media{Kind: kind, Reference: "/bridge/v1/media/" + p.ArtifactDigest, Mime: mime}, Placard: placard, Fit: p.Fit, Loop: p.Loop, Muted: p.Mute, Volume: p.Volume * 100})
		}
	}
	return json.Marshal(result)
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
func verifyFileDigest(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != expected {
		return errors.New("digest mismatch")
	}
	return nil
}
func verifyBytesDigest(data []byte, expected string) error {
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		return errors.New("digest mismatch")
	}
	return nil
}
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func validateRevision(data []byte, candidate Candidate) error {
	var value struct {
		Schema           string `json:"schema"`
		ShellBuildDigest string `json:"shellBuildDigest"`
		Entry            string `json:"entrypoint"`
		Bridge           string `json:"bridgeProtocol"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("decode revision artifact: %w", err)
	}
	if value.Schema != "cable.revision.v1" {
		return errors.New("unsupported cable revision schema")
	}
	if value.ShellBuildDigest != "" && value.ShellBuildDigest != candidate.ShellBuildDigest {
		return errors.New("revision shell digest mismatch")
	}
	if value.Entry != "" && value.Entry != candidate.Entrypoint {
		return errors.New("revision entrypoint mismatch")
	}
	if value.Bridge != "" && value.Bridge != candidate.BridgeProtocol {
		return errors.New("revision bridge protocol mismatch")
	}
	return nil
}

var _ Adapter = (*Chromium)(nil)
