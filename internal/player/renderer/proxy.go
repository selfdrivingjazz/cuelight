package renderer

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jmilldotdev/cuelight/internal/release"
)

type Proxy struct {
	upstream *url.URL
	client   *http.Client
	now      func() time.Time
	mu       sync.RWMutex
	grants   map[string]proxyGrant
}

type proxyGrant struct {
	digest  string
	expires time.Time
}

func NewProxy(upstream string, client *http.Client) (*Proxy, error) {
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("absolute upstream URL is required")
	}
	if client == nil {
		return nil, errors.New("authenticated upstream client is required")
	}
	return &Proxy{upstream: parsed, client: client, now: time.Now, grants: map[string]proxyGrant{}}, nil
}

func (p *Proxy) Grant(digest string, ttl time.Duration) (string, error) {
	if err := release.ValidateDigest(digest); err != nil {
		return "", err
	}
	if ttl <= 0 {
		return "", errors.New("positive TTL is required")
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	p.mu.Lock()
	p.grants[token] = proxyGrant{digest: digest, expires: p.now().Add(ttl)}
	p.mu.Unlock()
	return "/media/" + token + "/" + digest, nil
}

func (p *Proxy) Renew(path string, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("positive TTL is required")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 3 || parts[0] != "media" {
		return errors.New("invalid media capability path")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	grant, ok := p.grants[parts[1]]
	if !ok || grant.digest != parts[2] || !p.now().Before(grant.expires) {
		return errors.New("media capability is invalid or expired")
	}
	grant.expires = p.now().Add(ttl)
	p.grants[parts[1]] = grant
	return nil
}

func (p *Proxy) Revoke(path string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 3 {
		return
	}
	p.mu.Lock()
	delete(p.grants, parts[1])
	p.mu.Unlock()
}

func (p *Proxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "media" {
		http.NotFound(writer, request)
		return
	}
	token, digest := parts[1], parts[2]
	p.mu.RLock()
	grant, ok := p.grants[token]
	p.mu.RUnlock()
	if !ok || grant.digest != digest || !p.now().Before(grant.expires) {
		http.Error(writer, "media capability is invalid or expired", http.StatusUnauthorized)
		return
	}
	upstream := *p.upstream
	upstream.Path = strings.TrimRight(upstream.Path, "/") + "/player/artifacts/" + digest + "/content"
	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, upstream.String(), nil)
	if err != nil {
		http.Error(writer, "build upstream request", http.StatusBadGateway)
		return
	}
	for _, header := range []string{"Range", "If-Range"} {
		if value := request.Header.Get(header); value != "" {
			upstreamRequest.Header.Set(header, value)
		}
	}
	response, err := p.client.Do(upstreamRequest)
	if err != nil {
		http.Error(writer, "upstream media request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for _, header := range []string{"Accept-Ranges", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified"} {
		if value := response.Header.Get(header); value != "" {
			writer.Header().Set(header, value)
		}
	}
	writer.WriteHeader(response.StatusCode)
	if request.Method == http.MethodGet {
		_, _ = io.Copy(writer, response.Body)
	}
}
