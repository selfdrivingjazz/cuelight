package webitem

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/jmill/cable/internal/control/media"
)

// ChromiumCapturer renders pages with a local headless Chromium binary at a
// fixed 1920x1080 viewport, matching what a display node shows.
type ChromiumCapturer struct {
	Binary string
	Width  int
	Height int
	// SettleMillis is the virtual time budget given to the page before capture.
	SettleMillis int
}

// FindChromium returns the configured binary or the first Chromium-family
// browser on PATH; empty means server-side capture is unavailable.
func FindChromium(configured string) string {
	if configured != "" {
		return configured
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "chrome-headless-shell"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func (c ChromiumCapturer) args(extra ...string) []string {
	width, height, settle := c.Width, c.Height, c.SettleMillis
	if width <= 0 || height <= 0 {
		width, height = 1920, 1080
	}
	if settle <= 0 {
		settle = 5000
	}
	return append([]string{
		"--headless", "--no-sandbox", "--disable-gpu", "--hide-scrollbars", "--mute-audio",
		"--no-first-run", "--disable-extensions", "--disable-dev-shm-usage",
		fmt.Sprintf("--window-size=%d,%d", width, height),
		fmt.Sprintf("--virtual-time-budget=%d", settle),
	}, extra...)
}

func (c ChromiumCapturer) Capture(ctx context.Context, pageURL string) (Capture, error) {
	if c.Binary == "" {
		return Capture{}, media.ErrCaptureDisabled
	}
	dir, err := os.MkdirTemp("", "web-capture-")
	if err != nil {
		return Capture{}, err
	}
	defer os.RemoveAll(dir)
	shot := filepath.Join(dir, "viewport.png")

	var wg sync.WaitGroup
	var dom []byte
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Metadata is best effort; a failure leaves title/favicon empty.
		dom, _ = c.run(ctx, dir, "dom", "--dump-dom", pageURL)
	}()
	_, shotErr := c.run(ctx, dir, "shot", "--screenshot="+shot, pageURL)
	wg.Wait()
	png, readErr := os.ReadFile(shot)
	if readErr != nil || len(png) == 0 {
		if shotErr == nil {
			shotErr = errors.New("browser produced no screenshot")
		}
		return Capture{}, fmt.Errorf("capture %s: %w", pageURL, shotErr)
	}
	title, favicon := pageMetadata(dom, pageURL)
	return Capture{PNG: png, Title: title, FaviconURL: favicon}, nil
}

func (c ChromiumCapturer) run(ctx context.Context, dir, profile string, extra ...string) ([]byte, error) {
	args := c.args(append([]string{"--user-data-dir=" + filepath.Join(dir, profile)}, extra...)...)
	cmd := exec.CommandContext(ctx, c.Binary, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 400 {
			detail = detail[len(detail)-400:]
		}
		return stdout.Bytes(), fmt.Errorf("%w: %s", err, detail)
	}
	return stdout.Bytes(), nil
}

var (
	titlePattern = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	linkPattern  = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	relPattern   = regexp.MustCompile(`(?is)\brel\s*=\s*["']([^"']*)["']`)
	hrefPattern  = regexp.MustCompile(`(?is)\bhref\s*=\s*["']([^"']*)["']`)
)

func pageMetadata(dom []byte, pageURL string) (string, string) {
	var title string
	if match := titlePattern.FindSubmatch(dom); match != nil {
		title = strings.Join(strings.Fields(html.UnescapeString(string(match[1]))), " ")
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return title, ""
	}
	favicon := base.ResolveReference(&url.URL{Path: "/favicon.ico"}).String()
	for _, link := range linkPattern.FindAll(dom, -1) {
		rel := relPattern.FindSubmatch(link)
		href := hrefPattern.FindSubmatch(link)
		if rel == nil || href == nil {
			continue
		}
		rels := strings.Fields(strings.ToLower(string(rel[1])))
		isIcon := false
		for _, value := range rels {
			if value == "icon" {
				isIcon = true
			}
		}
		if !isIcon {
			continue
		}
		if ref, err := url.Parse(html.UnescapeString(string(href[1]))); err == nil {
			if resolved := base.ResolveReference(ref); resolved.Scheme == "http" || resolved.Scheme == "https" {
				favicon = resolved.String()
				break
			}
		}
	}
	return title, favicon
}
