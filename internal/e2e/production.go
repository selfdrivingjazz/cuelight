package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/client"
	"github.com/selfdrivingjazz/cuelight/internal/dev"
	"github.com/selfdrivingjazz/cuelight/internal/fixtures"
)

// NewProduction creates a runner backed by dev.Manager and the authenticated
// public operator client. The client is initialized after lifecycle startup so
// it can use the generated endpoint and admin credential without exposing it
// in Evidence.
func NewProduction(config dev.Config, httpClient *http.Client, evidence io.Writer) (*Runner, error) {
	manager, err := dev.New(config, dev.Options{})
	if err != nil {
		return nil, err
	}
	catalog, err := fixtures.Load()
	if err != nil {
		return nil, err
	}
	return New(Options{
		Lifecycle: manager,
		Client:    &productionClient{manager: manager, config: config, httpClient: httpClient},
		Catalog:   catalog,
		Evidence:  evidence,
		Root:      config.Root,
	})
}

type productionClient struct {
	manager    *dev.Manager
	config     dev.Config
	httpClient *http.Client
	api        *client.Client
}

func (p *productionClient) ensure(ctx context.Context) error {
	if p.api != nil {
		return nil
	}
	status, err := p.manager.Status(ctx)
	if err != nil {
		return err
	}
	if status.Endpoint == "" {
		return fmt.Errorf("development lifecycle has no control endpoint")
	}
	token, err := os.ReadFile(filepath.Join(p.config.Root, "admin-token"))
	if err != nil {
		return fmt.Errorf("read development credential: %w", err)
	}
	transportClient := p.httpClient
	if transportClient == nil {
		transportClient = &http.Client{}
	}
	copyClient := *transportClient
	transport, ok := copyClient.Transport.(*http.Transport)
	if !ok || transport == nil {
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			transport = base.Clone()
		} else {
			transport = &http.Transport{}
		}
	}
	caBytes, err := os.ReadFile(filepath.Join(p.config.Root, "trust", "ca.pem"))
	if err != nil {
		return fmt.Errorf("read development CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caBytes) {
		return fmt.Errorf("read development CA: invalid certificate")
	}
	transport.TLSClientConfig = (&tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}).Clone()
	copyClient.Transport = transport
	api, err := client.New(status.Endpoint+"/api/v1", strings.TrimSpace(string(token)), &copyClient)
	if err != nil {
		return err
	}
	p.api = api
	return nil
}
func (p *productionClient) ListNodes(ctx context.Context) ([]client.Node, error) {
	if err := p.ensure(ctx); err != nil {
		return nil, err
	}
	return p.api.ListNodes(ctx)
}
func (p *productionClient) CreateUpload(ctx context.Context, n string, l int64, m, k string) (client.Upload, error) {
	if err := p.ensure(ctx); err != nil {
		return client.Upload{}, err
	}
	return p.api.CreateUpload(ctx, n, l, m, k)
}
func (p *productionClient) AppendUpload(ctx context.Context, id string, o, l int64, b io.Reader) (int64, error) {
	if err := p.ensure(ctx); err != nil {
		return 0, err
	}
	return p.api.AppendUpload(ctx, id, o, l, b)
}
func (p *productionClient) CompleteUpload(ctx context.Context, id, k string) (client.Asset, error) {
	if err := p.ensure(ctx); err != nil {
		return client.Asset{}, err
	}
	return p.api.CompleteUpload(ctx, id, k)
}
func (p *productionClient) PublishRelease(ctx context.Context, b json.RawMessage, k string) (client.Document, error) {
	if err := p.ensure(ctx); err != nil {
		return nil, err
	}
	return p.api.PublishRelease(ctx, b, k)
}
func (p *productionClient) CreateMedia(ctx context.Context, input any, k string) (client.Document, error) {
	if err := p.ensure(ctx); err != nil {
		return nil, err
	}
	return p.api.CreateMedia(ctx, input, k)
}
func (p *productionClient) MediaForAsset(ctx context.Context, assetID string) (client.Document, bool, error) {
	if err := p.ensure(ctx); err != nil {
		return nil, false, err
	}
	return p.api.MediaForAsset(ctx, assetID)
}
func (p *productionClient) PutAssignment(ctx context.Context, n, match, k string, i client.AssignmentInput) (client.Assignment, error) {
	if err := p.ensure(ctx); err != nil {
		return client.Assignment{}, err
	}
	return p.api.PutAssignment(ctx, n, match, k, i)
}
func (p *productionClient) GetStatus(ctx context.Context, n string) (client.Document, error) {
	if err := p.ensure(ctx); err != nil {
		return nil, err
	}
	return p.api.GetStatus(ctx, n)
}
func (p *productionClient) ListEvents(ctx context.Context, n, c string) (client.EventPage, error) {
	if err := p.ensure(ctx); err != nil {
		return client.EventPage{}, err
	}
	return p.api.ListEvents(ctx, n, c)
}

var _ API = (*productionClient)(nil)
