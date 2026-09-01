package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmill/cable/internal/media/localcas"
	playerclient "github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/player/renderer"
	playerruntime "github.com/jmill/cable/internal/player/runtime"
	playerstate "github.com/jmill/cable/internal/player/state"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Printf("cable-player %s\n", version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	if len(os.Args) >= 2 && os.Args[1] == "provision" {
		if err := runProvision(ctx, os.Args[2:], os.LookupEnv, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: cable-player [--version] | cable-player provision [flags]")
		os.Exit(2)
	}
	if err := run(ctx, os.LookupEnv); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, lookup func(string) (string, bool)) error {
	config, err := loadConfig(lookup)
	if err != nil {
		return err
	}
	serverCA, err := os.ReadFile(config.ServerCAPath)
	if err != nil {
		return fmt.Errorf("read player server CA: %w", err)
	}
	nodeCertificate, err := tls.LoadX509KeyPair(config.NodeCertificatePath, config.NodePrivateKeyPath)
	if err != nil {
		return fmt.Errorf("load player node credential: %w", err)
	}
	publisherKey, err := readPublisherKey(config.PublisherPublicKeyPath)
	if err != nil {
		return err
	}

	controller, err := playerclient.New(playerclient.Config{BaseURL: config.ControlURL, ServerCAPEM: serverCA, NodeCertificate: &nodeCertificate})
	if err != nil {
		return fmt.Errorf("create player client: %w", err)
	}
	defer controller.CloseIdleConnections()
	proxyHTTPClient, err := authenticatedHTTPClient(serverCA, nodeCertificate)
	if err != nil {
		return err
	}
	proxy, err := renderer.NewProxy(config.ControlURL, proxyHTTPClient)
	if err != nil {
		return fmt.Errorf("create stream proxy: %w", err)
	}
	listener, err := net.Listen("tcp", config.LoopbackAddress)
	if err != nil {
		return fmt.Errorf("listen for loopback stream proxy: %w", err)
	}
	streamer := &loopbackStreamer{proxy: proxy, base: (&url.URL{Scheme: "http", Host: listener.Addr().String()}).String()}
	proxyServer := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}

	if err := os.MkdirAll(filepath.Dir(config.StatePath), 0o700); err != nil {
		listener.Close()
		return fmt.Errorf("create player state directory: %w", err)
	}
	journal, err := playerstate.Open(config.StatePath)
	if err != nil {
		listener.Close()
		return err
	}
	defer journal.Close()
	cache, err := localcas.New(config.CASPath)
	if err != nil {
		listener.Close()
		return fmt.Errorf("open player cache: %w", err)
	}
	if config.RendererMode != "mpv-only" {
		if err := os.MkdirAll(config.ChromiumRuntimeRoot, 0o700); err != nil {
			listener.Close()
			return fmt.Errorf("create Chromium runtime root: %w", err)
		}
	}
	var mediaRenderer, cableRenderer renderer.Adapter
	if config.RendererMode == "desktop" {
		launcher, err := renderer.NewHTTPBrowserLauncher(config.DesktopRendererURL, config.DesktopRendererToken, &http.Client{Timeout: config.ChromiumTimeout})
		if err != nil {
			listener.Close()
			return fmt.Errorf("create desktop browser launcher: %w", err)
		}
		browserRenderer, err := renderer.NewChromium(renderer.ChromiumConfig{RuntimeRoot: config.ChromiumRuntimeRoot, Timeout: config.ChromiumTimeout, Launcher: launcher, Output: os.Stderr, Remote: controller})
		if err != nil {
			listener.Close()
			return fmt.Errorf("create desktop renderer: %w", err)
		}
		mediaRenderer, cableRenderer = browserRenderer, browserRenderer
	} else {
		mediaRenderer = &renderer.MPV{SocketPath: config.MPVSocketPath, Timeout: config.MPVTimeout}
		if config.RendererMode == "mpv-only" {
			cableRenderer = nil
		} else {
			browserRenderer, err := renderer.NewChromium(renderer.ChromiumConfig{BinaryPath: config.ChromiumBinaryPath, RuntimeRoot: config.ChromiumRuntimeRoot, Timeout: config.ChromiumTimeout, ExtraArgs: config.ChromiumArgs, Output: os.Stderr, Remote: controller})
			if err != nil {
				listener.Close()
				return fmt.Errorf("create Chromium renderer: %w", err)
			}
			cableRenderer = browserRenderer
		}
	}
	var rendererAdapter renderer.Adapter
	if config.RendererMode == "mpv-only" {
		rendererAdapter = mediaRenderer
	} else {
		composite, err := renderer.NewComposite(mediaRenderer, cableRenderer)
		if err != nil {
			listener.Close()
			return fmt.Errorf("create renderer dispatcher: %w", err)
		}
		rendererAdapter = composite
	}
	reconciler, err := playerruntime.New(config.runtimeConfig(publisherKey), playerruntime.Dependencies{
		Controller: controller, Journal: journal, CAS: cache, Capacity: filesystemCapacity{path: config.CASPath}, Streamer: streamer, Renderer: rendererAdapter,
	})
	if err != nil {
		listener.Close()
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErrors := make(chan error, 1)
	go func() {
		err := proxyServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErrors <- err
	}()
	runtimeErrors := make(chan error, 1)
	go func() { runtimeErrors <- reconciler.Run(runCtx) }()

	var result error
	select {
	case result = <-runtimeErrors:
		cancel()
	case result = <-serveErrors:
		cancel()
		if result != nil {
			result = fmt.Errorf("loopback stream proxy stopped: %w", result)
		}
	case <-ctx.Done():
		cancel()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownErr := errors.Join(rendererAdapter.Stop(shutdownCtx), proxyServer.Shutdown(shutdownCtx))
	shutdownCancel()
	if result == nil || errors.Is(result, context.Canceled) {
		result = nil
	}
	if shutdownErr != nil && !errors.Is(shutdownErr, context.Canceled) {
		result = errors.Join(result, shutdownErr)
	}
	return result
}

func authenticatedHTTPClient(serverCA []byte, certificate tls.Certificate) (*http.Client, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(serverCA) {
		return nil, errors.New("player server CA is not valid PEM")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, Certificates: []tls.Certificate{certificate}}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func readPublisherKey(path string) (ed25519.PublicKey, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read publisher public key: %w", err)
	}
	trimmed := strings.TrimSpace(string(encoded))
	if block, _ := pem.Decode(encoded); block != nil {
		parsed, parseErr := x509.ParsePKIXPublicKey(block.Bytes)
		if parseErr != nil {
			return nil, errors.New("publisher public key PEM is invalid")
		}
		key, ok := parsed.(ed25519.PublicKey)
		if !ok {
			return nil, errors.New("publisher public key is not Ed25519")
		}
		return key, nil
	}
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("publisher public key must be Ed25519 PKIX PEM or base64 raw key")
	}
	return ed25519.PublicKey(raw), nil
}

type loopbackStreamer struct {
	proxy *renderer.Proxy
	base  string
}

func (s *loopbackStreamer) Open(_ context.Context, digest string, ttl time.Duration) (string, func(), error) {
	path, err := s.proxy.Grant(digest, ttl)
	if err != nil {
		return "", nil, err
	}
	return s.base + path, func() { s.proxy.Revoke(path) }, nil
}

func (s *loopbackStreamer) Renew(_ context.Context, source string, ttl time.Duration) error {
	parsed, err := url.Parse(source)
	if err != nil {
		return errors.New("invalid loopback stream source")
	}
	base, err := url.Parse(s.base)
	if err != nil || parsed.Scheme != base.Scheme || parsed.Host != base.Host {
		return errors.New("stream source does not belong to loopback proxy")
	}
	return s.proxy.Renew(parsed.Path, ttl)
}
