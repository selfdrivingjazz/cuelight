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
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	apispec "github.com/jmilldotdev/cuelight/api"
	"github.com/jmilldotdev/cuelight/internal/buildinfo"
	"github.com/jmilldotdev/cuelight/internal/control/app"
	"github.com/jmilldotdev/cuelight/internal/control/appaccess"
	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	"github.com/jmilldotdev/cuelight/internal/control/auth"
	"github.com/jmilldotdev/cuelight/internal/control/media"
	"github.com/jmilldotdev/cuelight/internal/control/operatorapi"
	"github.com/jmilldotdev/cuelight/internal/control/persistence"
	"github.com/jmilldotdev/cuelight/internal/control/pki"
	"github.com/jmilldotdev/cuelight/internal/control/playerapi"
	"github.com/jmilldotdev/cuelight/internal/control/preview"
	"github.com/jmilldotdev/cuelight/internal/control/profile"
	controlrelease "github.com/jmilldotdev/cuelight/internal/control/release"
	"github.com/jmilldotdev/cuelight/internal/control/remote"
	"github.com/jmilldotdev/cuelight/internal/control/software"
	"github.com/jmilldotdev/cuelight/internal/control/store"
	"github.com/jmilldotdev/cuelight/internal/control/usage"
	"github.com/jmilldotdev/cuelight/internal/control/webitem"
	"github.com/jmilldotdev/cuelight/internal/control/webnode"
	"github.com/jmilldotdev/cuelight/internal/control/webui"
	"github.com/jmilldotdev/cuelight/internal/media/localcas"
	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	"github.com/jmilldotdev/cuelight/internal/protocol"
	canonical "github.com/jmilldotdev/cuelight/internal/release"
	"github.com/jmilldotdev/cuelight/migrations"
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
	previewRepository := store.NewPreviewRepository(repository)
	previewRepository.CAS = cas
	previewService := &preview.Service{Repository: previewRepository, Artifacts: previewRepository, Sink: previewRepository, Reader: previewRepository, Config: preview.Config{DefaultTTL: 5 * time.Minute, MaxTTL: 15 * time.Minute, RetentionTTL: 24 * time.Hour, ArtifactRetentionTTL: 24 * time.Hour}}
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
	targetPublisher := controlrelease.TargetPublisher{
		Media:   controlrelease.Service{Releases: repository, Artifacts: repository, Profiles: staticProfile{minimum: config.ProtocolVersion}, Signer: signer},
		Catalog: repository, Releases: repository, Signer: signer,
		MinimumProtocolVersion: config.ProtocolVersion,
	}
	assignmentService := assignment.Service{Assignments: repository, Releases: targetPublisher}
	playerService, err := playerapi.NewService(repository, repository, store.NewLocalCASReader(cas), playerapi.Limits{MaxBodyBytes: config.MaxBodyBytes, MaxEvents: config.MaxEvents, MaxWait: config.MaxWait})
	if err != nil {
		return nil, fmt.Errorf("construct player service: %w", err)
	}
	softwareService := &software.Service{
		Repository: repository, Blobs: cas, PublicURL: config.PublicURL, ControlVersion: buildinfo.Version,
		Feed: software.Feed{URL: config.UpdateFeedURL, Enabled: config.UpdateCheck, Interval: config.UpdateCheckInterval},
	}
	playerService.Software = softwareService
	go softwareService.RunFeed(context.Background())

	remoteService := &remote.Service{Policies: repository, PublicURL: config.PublicURL, TTL: 12 * time.Hour}
	operatorRepository := store.NewOperatorAdapter(repository)
	authoredMediaService, err := media.NewAuthoredService(operatorRepository, randomID)
	if err != nil {
		return nil, fmt.Errorf("construct authored media service: %w", err)
	}
	webCapture := &webitem.Service{Repository: &store.WebCaptureRepository{Store: repository, CAS: cas}}
	if browser := webitem.FindChromium(config.WebCaptureBrowser); browser != "" {
		webCapture.Capturer = webitem.ChromiumCapturer{Binary: browser}
	}
	go webCapture.Run(context.Background(), time.Minute)
	mediaScopeAuthorizer := operatorapi.ScopeAuthorizer{Scopes: map[string]string{
		operatorapi.ScopeMediaRead:  operatorapi.ScopeMediaRead,
		operatorapi.ScopeMediaWrite: operatorapi.ScopeMediaWrite,
	}}
	mediaThumbnails := &media.ThumbnailService{Repository: repository, CAS: cas, Content: media.NewLocalCASContent(cas)}
	go mediaThumbnails.Backfill(context.Background(), slog.Default())
	authoredMediaHandler := media.NewOperatorHandler(media.OperatorDependencies{
		Service:    authoredMediaService,
		Content:    media.NewLocalCASContent(cas),
		Thumbnails: mediaThumbnails,
		Web:        webCapture,
		Authorize: func(actor auth.Actor, action, resourceType, resourceID string) error {
			return mediaScopeAuthorizer.Authorize(actor, action, resourceType, resourceID)
		},
		MaxJSONBytes: config.MaxBodyBytes,
	})
	var joinLinks operatorapi.JoinLinks
	if config.PublicURL != "" {
		chainPEM, err := os.ReadFile(config.TLSCertificatePath)
		if err != nil {
			return nil, fmt.Errorf("read TLS chain for join links: %w", err)
		}
		if joinLinks, err = operatorapi.NewJoinLinks(config.PublicURL, config.NetworkName, chainPEM); err != nil {
			return nil, fmt.Errorf("construct join links: %w", err)
		}
	}
	operatorDependencies := operatorapi.Dependencies{
		Auth:            authService,
		PKI:             pkiService,
		RemoteLinks:     remoteService,
		Nodes:           operatorRepository,
		Assignments:     repository,
		Assigner:        assignmentService,
		AssignmentPages: repository,
		Media:           mediaService,
		Uploads:         repository,
		Registration:    registration,
		Assets:          repository,
		Releases:        releaseService,
		Playlists:       repository,
		ReleasePages:    repository,
		MaxJSONBytes:    config.MaxBodyBytes,
		MaxChunkBytes:   config.MaxChunkBytes,
		Idempotency:     operatorapi.NewIdempotencyCache(),
		Profiles:        profile.Service{Repository: store.ProfileRepository{Store: repository}, Assigner: assignmentService},
		JoinLinks:       joinLinks,
	}
	operator := operatorapi.NewHandler(operatorDependencies)
	usageService := usage.NewService(repository, nil)
	usageHandler := usage.NewHandler(usageService)
	api := http.NewServeMux()
	pkiHandler := newPKIHandler(pkiService, config.MaxBodyBytes)
	api.Handle("/player/enroll", pkiHandler)
	api.Handle("/player/recover", pkiHandler)
	api.Handle("/player/credentials:rotate", pkiHandler)
	api.Handle("/player/credentials/rotate", pkiHandler)
	remoteHandler := remote.NewHandler(remoteService, nil)
	api.Handle("/player/remote-sessions", remoteHandler)
	api.Handle("/player/remote-sessions/", remoteHandler)
	api.Handle("/remote/", remoteHandler)
	appService := &appaccess.Service{Repository: repository}
	if config.PublicURL != "" {
		appService.APIURL = strings.TrimRight(config.PublicURL, "/") + "/api/v1"
	}
	api.Handle("/player/app-tokens", appaccess.NewPlayerHandler(appService, func(r *http.Request) (string, error) { return protocol.NodeIdentity(r, nil) }))
	api.Handle("/media", operatorDependencies.Middleware(authoredMediaHandler))
	playerScreenshotHandler := preview.NewPlayerHandler(previewService, func(r *http.Request) (string, error) { return protocol.NodeIdentity(r, nil) })
	api.Handle("/player/screenshots/", playerScreenshotHandler)
	operatorScreenshotHandler := preview.NewOperatorHandler(previewService)
	api.Handle("/screenshots", operatorDependencies.MiddlewareForScope(operatorapi.ScopeNodeWrite, operatorScreenshotHandler))
	api.Handle("/screenshots/", operatorDependencies.MiddlewareForScope(operatorapi.ScopeNodeRead, operatorScreenshotHandler))
	api.Handle("/media/", operatorDependencies.Middleware(authoredMediaHandler))
	api.Handle("/usage/", operatorDependencies.MiddlewareForScope(operatorapi.ScopeMediaRead, usageHandler))
	playerHandler := playerapi.NewHandler(playerService)
	api.Handle("/player/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		playerHandler.ServeHTTP(w, r)
	}))
	api.Handle("/player/software/", software.NewPlayerHandler(softwareService, func(r *http.Request) (string, error) { return protocol.NodeIdentity(r, nil) }))
	softwareOperator := software.NewOperatorHandler(softwareService, software.OperatorOptions{
		Authorize: operatorDependencies.MiddlewareForScope, ReadScope: operatorapi.ScopeNodeRead, WriteScope: operatorapi.ScopeNodeWrite,
		MaxUploadBytes: config.MaxUploadBytes, MaxJSONBytes: config.MaxBodyBytes,
	})
	api.Handle("/software", softwareOperator)
	api.Handle("/software/", softwareOperator)
	browserNodes := &webnode.Service{Repository: repository, Config: webnode.Config{PairBaseURL: config.PublicURL}, NewID: newUUID}
	browserPlayerService := *playerService
	browserPlayerService.Identity = browserNodes.Identity
	browserPlayer := http.NewServeMux()
	browserPlayerRemote := remote.NewHandler(remoteService, browserNodes.Identity)
	browserPlayer.Handle("/player/remote-sessions", browserPlayerRemote)
	browserPlayer.Handle("/player/remote-sessions/", browserPlayerRemote)
	browserPlayer.Handle("/player/app-tokens", appaccess.NewPlayerHandler(appService, browserNodes.Identity))
	browserPlayer.Handle("/player/", playerapi.NewHandler(&browserPlayerService))
	api.Handle("/browser-node/", http.StripPrefix("/browser-node", webnode.NewBrowserHandler(browserNodes, browserPlayer)))
	appMediaHandler := media.NewOperatorHandler(media.OperatorDependencies{
		Service:    authoredMediaService,
		Content:    media.NewLocalCASContent(cas),
		Thumbnails: mediaThumbnails,
		Authorize: func(_ auth.Actor, action, _, _ string) error {
			if action != operatorapi.ScopeMediaRead {
				return media.ErrForbidden
			}
			return nil
		},
		MaxJSONBytes: config.MaxBodyBytes,
	})
	api.Handle("/app/", appaccess.NewAppHandler(appaccess.AppDependencies{
		Service:   appService,
		Media:     appMediaHandler,
		Playlists: repository,
		Remote:    remoteService,
	}))
	api.Handle("GET /openapi.yaml", appaccess.WithCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(apispec.OpenAPI)
	})))
	browserNodeOperator := webnode.NewOperatorHandler(browserNodes, operatorDependencies.MiddlewareForScope, operatorapi.ScopeNodeEnroll, operatorapi.ScopeNodeRead, operatorapi.ScopeSecurityAdmin)
	api.Handle("/browser-nodes", browserNodeOperator)
	api.Handle("/browser-nodes/", browserNodeOperator)
	api.Handle("/", operator)
	root := http.NewServeMux()
	root.Handle("/api/v1/", http.StripPrefix("/api/v1", api))
	root.Handle("/updates/", software.NewPublicHandler(softwareService))
	root.Handle("/node/", http.StripPrefix("/node", webui.Handler(webui.WebsiteNode)))
	root.Handle("/node", http.RedirectHandler("/node/", http.StatusMovedPermanently))
	root.Handle("/", webui.Handler(webui.ControllerWeb))
	return withVersionHeader(root), nil
}

// withVersionHeader stamps every response with control's software version;
// browser nodes reload when it differs from the build they run.
func withVersionHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Cuelight-Version", buildinfo.Version)
		next.ServeHTTP(w, r)
	})
}

func newUUID() (string, error) {
	if id := randomID(); id != "" {
		return id, nil
	}
	return "", errors.New("generate UUID: random source failed")
}

func newPKIHandler(service *pki.Service, maxBody int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			http.Error(w, "PKI service is not configured", http.StatusInternalServerError)
			return
		}
		key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		switch r.URL.Path {
		case "/player/enroll", "/player/recover":
			var input playerclient.EnrollmentRequest
			if err := decodePKIJSON(w, r, maxBody, &input); err != nil {
				writePKIError(w, err)
				return
			}
			publicKey, err := decodePublicKey(input.PublicKey)
			if err != nil {
				writePKIError(w, err)
				return
			}
			if r.URL.Path == "/player/enroll" {
				result, err := service.Enroll(r.Context(), pki.EnrollRequest{Token: input.Token, IdempotencyKey: key, Name: input.Name, Credential: pki.CredentialRequest{PublicKey: publicKey}})
				if err != nil {
					writePKIError(w, err)
					return
				}
				writePKIJSON(w, http.StatusCreated, result)
				return
			}
			result, err := service.Recover(r.Context(), pki.RecoverRequest{Token: input.Token, IdempotencyKey: key, Credential: pki.CredentialRequest{PublicKey: publicKey}})
			if err != nil {
				writePKIError(w, err)
				return
			}
			writePKIJSON(w, http.StatusCreated, result.Enrollment)
		case "/player/credentials:rotate", "/player/credentials/rotate":
			nodeID, err := protocol.NodeIdentity(r, nil)
			if err != nil {
				writePKIError(w, fmt.Errorf("%w: %v", playerapi.ErrUnauthorized, err))
				return
			}
			if len(r.TLS.PeerCertificates) == 0 {
				writePKIError(w, playerapi.ErrUnauthorized)
				return
			}
			publicKeyDER, err := x509.MarshalPKIXPublicKey(r.TLS.PeerCertificates[0].PublicKey)
			if err != nil {
				writePKIError(w, err)
				return
			}
			result, err := service.Rotate(r.Context(), pki.RotateRequest{NodeID: nodeID, PreviousSerial: r.TLS.PeerCertificates[0].SerialNumber.String(), IdempotencyKey: key, Credential: pki.CredentialRequest{PublicKey: publicKeyDER}})
			if err != nil {
				writePKIError(w, err)
				return
			}
			writePKIJSON(w, http.StatusCreated, result.ClientCredential)
		default:
			http.NotFound(w, r)
		}
	})
}

func decodePKIJSON(w http.ResponseWriter, r *http.Request, limit int64, target any) error {
	if limit <= 0 {
		limit = 4 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func decodePublicKey(value string) ([]byte, error) {
	if strings.TrimSpace(value) == "" {
		return nil, errors.New("public key is required")
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return []byte(value), nil
}

func writePKIError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, playerapi.ErrUnauthorized) {
		status = http.StatusUnauthorized
	}
	if errors.Is(err, pki.ErrNodeRevoked) || errors.Is(err, pki.ErrCredentialRevoked) {
		status = http.StatusForbidden
	}
	http.Error(w, err.Error(), status)
}
func writePKIJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// ConfigureTLS configures direct TLS client-CA verification without requiring
// mTLS on bearer-authenticated operator requests. A presented client cert is
// always verified; player handlers additionally require its Cuelight node SAN.

// BootstrapAdminToken initializes an actor and creates one administrator
// credential. The returned Secret is intentionally never persisted and should
// be displayed exactly once by the caller.
func BootstrapAdminToken(ctx context.Context, config app.Config, actorID, actorName string, scopes []string) (auth.CreatedToken, error) {
	if strings.TrimSpace(config.DatabaseURL) == "" {
		return auth.CreatedToken{}, &app.ConfigError{Field: "CUELIGHT_CONTROL_DATABASE_URL", Reason: "is required"}
	}
	if actorID == "" {
		actorID = randomID()
	}
	if actorName == "" {
		actorName = "cuelight-control administrator"
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
	if err := persistence.ApplyMigrations(migrationCtx, db, migrations.FS); err != nil {
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

// ImportSoftwareRelease imports a release directory (cuelight-release.json
// plus artifacts) into control's database and CAS, as
// `cuelight-control updates import <dir>` does on the server.
func ImportSoftwareRelease(ctx context.Context, config app.Config, dir string) (software.ImportResult, error) {
	timeout := config.DatabaseTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dbCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	db, err := persistence.OpenPostgres(dbCtx, config.DatabaseURL, persistence.PoolConfig{MaxOpenConnections: 2, MaxIdleConnections: 1, ConnectionLifetime: time.Hour})
	if err != nil {
		return software.ImportResult{}, err
	}
	defer db.Close()
	if err := persistence.ApplyMigrations(dbCtx, db, migrations.FS); err != nil {
		return software.ImportResult{}, err
	}
	repository, err := store.New(db)
	if err != nil {
		return software.ImportResult{}, err
	}
	cas, err := localcas.New(config.CASPath)
	if err != nil {
		return software.ImportResult{}, fmt.Errorf("open CAS: %w", err)
	}
	service := &software.Service{Repository: repository, Blobs: cas, ControlVersion: buildinfo.Version}
	return service.ImportDir(ctx, software.SourceCLI, dir)
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
