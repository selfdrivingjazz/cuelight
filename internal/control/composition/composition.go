// Package composition owns the production control-plane dependency graph.
package composition

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/app"
	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/operatorapi"
	"github.com/jmill/cable/internal/control/persistence"
	"github.com/jmill/cable/internal/control/pki"
	"github.com/jmill/cable/internal/control/playerapi"
	controlrelease "github.com/jmill/cable/internal/control/release"
	"github.com/jmill/cable/internal/control/store"
	"github.com/jmill/cable/internal/media/localcas"
	canonical "github.com/jmill/cable/internal/release"
)

// HandlerFactory is suitable for app.Options.Handler.
func HandlerFactory(runtime app.Runtime) (http.Handler, error) {
	config := runtime.Config
	repository, err := store.New(runtime.Database)
	if err != nil {
		return nil, err
	}
	clock := pki.SystemClock()

	publisher, err := readEd25519PrivateKey(config.PublisherPrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load publisher key: %w", err)
	}
	publisherID := strings.TrimSpace(config.PublisherKeyID)
	if publisherID == "" {
		return nil, errors.New("publisher key ID is required")
	}

	caPEM, err := readPrivateAsset(config.CAPath)
	if err != nil {
		return nil, fmt.Errorf("load CA certificate: %w", err)
	}
	caKey, err := readCryptoPrivateKey(config.CAKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load CA key: %w", err)
	}
	authority, err := pki.NewCertificateAuthorityPEM(caPEM, caKey, 365*24*time.Hour, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("construct CA issuer: %w", err)
	}
	sealKey, err := readSealKey(config.SealKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load seal key: %w", err)
	}
	sealer, err := pki.NewAESGCMSealer(sealKey, rand.Reader)
	if err != nil {
		return nil, err
	}
	pkiService, err := pki.NewService(repository, authority, sealer, clock, pki.CryptoSerialSource{}, pki.RandomEpochSource{}, pki.PublisherKey{ID: publisherID, PublicKey: publisher.Public().(ed25519.PublicKey)}, pki.ServiceOptions{TokenLifetime: 24 * time.Hour, IdempotencyLifetime: 24 * time.Hour})
	if err != nil {
		return nil, fmt.Errorf("construct PKI service: %w", err)
	}
	authService := auth.NewService(repository, clock, rand.Reader)

	cas, err := localcas.New(config.CASPath)
	if err != nil {
		return nil, fmt.Errorf("construct CAS: %w", err)
	}
	temporary, err := newTemporaryStore(config.TemporaryRoot)
	if err != nil {
		return nil, fmt.Errorf("construct temporary store: %w", err)
	}
	probe := contentProbe{CAS: cas}
	mediaService, err := media.NewService(repository, temporary, cas, probe, randomID, media.Limits{MaxUploadBytes: config.MaxUploadBytes, MaxChunkBytes: config.MaxChunkBytes, MaxNameBytes: 255, MaxMediaTypeBytes: 255})
	if err != nil {
		return nil, fmt.Errorf("construct media service: %w", err)
	}
	registration, err := media.NewRegistrationService(repository, cas, probe, media.OSSafeOpener{}, media.StreamCopier{}, randomID, media.LocalRegistrationPolicy{Enabled: len(config.MediaRoots) > 0, AllowlistedRoots: config.MediaRoots, MaxBytes: config.MaxLocalMediaBytes})
	if err != nil {
		return nil, fmt.Errorf("construct local media registration: %w", err)
	}

	signer, err := controlrelease.NewEd25519Signer(publisherID, publisher)
	if err != nil {
		return nil, err
	}
	releaseService := releaseAdapter{service: controlrelease.Service{Releases: repository, Artifacts: repository, Profiles: staticProfile{minimum: config.ProtocolVersion}, Signer: signer}, repository: repository}
	playerService, err := playerapi.NewService(repository, repository, store.NewLocalCASReader(cas), playerapi.Limits{MaxBodyBytes: config.MaxBodyBytes, MaxEvents: config.MaxEvents, MaxWait: config.MaxWait})

	operatorRepository := store.NewOperatorAdapter(repository)
	operator := operatorapi.NewHandler(operatorapi.Dependencies{Auth: authService, PKI: pkiService, Nodes: operatorRepository, Assignments: repository, Media: mediaService, Registration: registration, Assets: repository, Releases: releaseService, MaxJSONBytes: config.MaxBodyBytes, MaxChunkBytes: config.MaxChunkBytes, Idempotency: operatorapi.NewIdempotencyCache()})
	root := http.NewServeMux()
	root.Handle("/api/v1/", http.StripPrefix("/api/v1", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/player/") {
			playerapi.NewHandler(playerService).ServeHTTP(w, r)
			return
		}
		operator.ServeHTTP(w, r)
	})))
	return root, nil
}

// ConfigureTLS configures direct TLS client-CA verification without requiring
// mTLS on bearer-authenticated operator requests. A presented client cert is
// always verified; player handlers additionally require its Cable node SAN.

// BootstrapAdminToken initializes an actor and creates one administrator
// credential. The returned Secret is intentionally never persisted and should
// be displayed exactly once by the caller.
func BootstrapAdminToken(ctx context.Context, config app.Config, actorID, actorName string, scopes []string) (auth.CreatedToken, error) {
	if strings.TrimSpace(config.DatabaseURL) == "" {
		return auth.CreatedToken{}, &app.ConfigError{Field: "CABLE_CONTROL_DATABASE_URL", Reason: "is required"}
	}
	if strings.TrimSpace(config.MigrationsPath) == "" {
		return auth.CreatedToken{}, &app.ConfigError{Field: "CABLE_CONTROL_MIGRATIONS_PATH", Reason: "is required"}
	}
	if actorID == "" {
		actorID = randomID()
	}
	if actorName == "" {
		actorName = "cable-control administrator"
	}
	timeout := config.DatabaseTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dbCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	db, err := persistence.OpenPostgres(dbCtx, config.DatabaseURL, persistence.PoolConfig{MaxOpenConnections: 2, MaxIdleConnections: 1, ConnectionLifetime: time.Hour})
	if err != nil {
		return auth.CreatedToken{}, err
	}
	defer db.Close()
	migrationCtx, cancelMigration := context.WithTimeout(ctx, timeout)
	defer cancelMigration()
	if err := persistence.ApplyMigrations(migrationCtx, db, os.DirFS(config.MigrationsPath)); err != nil {
		return auth.CreatedToken{}, err
	}
	repository, err := store.New(db)
	if err != nil {
		return auth.CreatedToken{}, err
	}
	if err := repository.EnsureActor(ctx, actorID, actorName); err != nil {
		return auth.CreatedToken{}, err
	}
	return auth.NewService(repository, pki.SystemClock(), rand.Reader).Create(ctx, actorID, scopes)
}
func ConfigureTLS(config app.Config) func(*tls.Config) error {
	return func(tlsConfig *tls.Config) error {
		if strings.TrimSpace(config.CAPath) == "" {
			return errors.New("CA path is required for TLS client verification")
		}
		data, err := os.ReadFile(config.CAPath)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return errors.New("CA file contains no certificates")
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.VerifyClientCertIfGiven
		tlsConfig.MinVersion = tls.VersionTLS13
		return nil
	}
}

type releaseAdapter struct {
	service    controlrelease.Service
	repository *store.Store
}

func (a releaseAdapter) Publish(ctx context.Context, mutation controlrelease.Mutation, input controlrelease.PublishInput) (controlrelease.PublishResult, error) {
	return a.service.Publish(ctx, mutation, input)
}
func (a releaseAdapter) GetRelease(ctx context.Context, id string) (canonical.EnvelopeV1, error) {
	return a.repository.LoadRelease(ctx, id)
}

type staticProfile struct{ minimum string }

func (p staticProfile) ProfileForPlayback(context.Context, canonical.PlaybackPayload) (controlrelease.Profile, error) {
	return controlrelease.Profile{MinimumProtocolVersion: p.minimum}, nil
}

type contentProbe struct{ CAS *localcas.Store }

func (p contentProbe) Probe(ctx context.Context, object localcas.Object) (media.ProbeCertification, error) {
	if err := ctx.Err(); err != nil {
		return media.ProbeCertification{}, err
	}
	file, err := os.Open(object.Path)
	if err != nil {
		return media.ProbeCertification{}, err
	}
	defer file.Close()
	var sample [512]byte
	n, err := file.Read(sample[:])
	if err != nil && err != io.EOF {
		return media.ProbeCertification{}, err
	}
	kind := http.DetectContentType(sample[:n])
	if kind == "application/octet-stream" {
		kind = "application/octet-stream"
	}
	return media.ProbeCertification{Certified: true, MediaType: kind, Profile: "default"}, nil
}

func randomID() string {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return ""
	}
	value[6] = value[6]&15 | 64
	value[8] = value[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}

func readPrivateAsset(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("symlinks are not allowed")
	}
	return os.ReadFile(path)
}
func readSealKey(path string) ([]byte, error) {
	data, err := readPrivateAsset(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == pki.AESGCMKeySize {
		return data, nil
	}
	return nil, errors.New("seal key must contain exactly 32 bytes")
}
func readCryptoPrivateKey(path string) (crypto.Signer, error) {
	data, err := readPrivateAsset(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("malformed private key PEM")
	}
	value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := value.(crypto.Signer)
	if !ok {
		return nil, errors.New("private key is not a signer")
	}
	return signer, nil
}
func readEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	signer, err := readCryptoPrivateKey(path)
	if err != nil {
		return nil, err
	}
	key, ok := signer.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("publisher key must be Ed25519")
	}
	return key, nil
}
