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
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/app"
	"github.com/jmill/cable/internal/control/auth"
	controlcable "github.com/jmill/cable/internal/control/cable"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/operatorapi"
	"github.com/jmill/cable/internal/control/persistence"
	"github.com/jmill/cable/internal/control/pki"
	"github.com/jmill/cable/internal/control/playerapi"
	controlrelease "github.com/jmill/cable/internal/control/release"
	"github.com/jmill/cable/internal/control/store"
	"github.com/jmill/cable/internal/media/localcas"
	playerclient "github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/protocol"
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
	cableService := &controlcable.Service{Repository: repository}
	revisionArtifacts := revisionArtifactAdapter{cas: cas, repository: repository}
	cableCompiler := &controlcable.Compiler{Repository: repository, Artifacts: repository, Builds: repository, RevisionArtifacts: revisionArtifacts}
	cablePublisher := &controlrelease.CablePublisher{
		Releases: repository, Revisions: repository, Artifacts: repository, Signer: signer,
		MinimumProtocolVersion: config.ProtocolVersion,
	}
	playerService, err := playerapi.NewService(repository, repository, store.NewLocalCASReader(cas), playerapi.Limits{MaxBodyBytes: config.MaxBodyBytes, MaxEvents: config.MaxEvents, MaxWait: config.MaxWait})

	operatorRepository := store.NewOperatorAdapter(repository)
	operator := operatorapi.NewHandler(operatorapi.Dependencies{
		Auth:            authService,
		PKI:             pkiService,
		Nodes:           operatorRepository,
		Assignments:     repository,
		AssignmentPages: repository,
		Media:           mediaService,
		Uploads:         repository,
		Registration:    registration,
		Assets:          repository,
		Releases:        releaseService,
		Cable:           cableService,
		CableRepository: repository,
		CableCompiler:   cableCompiler,
		CableReleases:   cablePublisher,
		ReleasePages:    repository,
		MaxJSONBytes:    config.MaxBodyBytes,
		MaxChunkBytes:   config.MaxChunkBytes,
		Idempotency:     operatorapi.NewIdempotencyCache(),
	})
	api := http.NewServeMux()
	pkiHandler := newPKIHandler(pkiService, config.MaxBodyBytes)
	api.Handle("/player/enroll", pkiHandler)
	api.Handle("/player/recover", pkiHandler)
	api.Handle("/player/credentials:rotate", pkiHandler)
	api.Handle("/player/credentials/rotate", pkiHandler)
	playerHandler := playerapi.NewHandler(playerService)
	api.Handle("/player/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		playerHandler.ServeHTTP(w, r)
	}))
	api.Handle("/", operator)
	root := http.NewServeMux()
	root.Handle("/api/v1/", http.StripPrefix("/api/v1", api))
	return root, nil
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
type revisionArtifactAdapter struct {
	cas        *localcas.Store
	repository *store.Store
}

func (a revisionArtifactAdapter) PutRevisionArtifact(ctx context.Context, digest string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	object, err := a.cas.Put(bytes.NewReader(content), digest)
	if err != nil {
		return err
	}
	if object.Digest != digest || object.Size != int64(len(content)) {
		return errors.New("revision artifact store returned inconsistent metadata")
	}
	return a.repository.RegisterCableRevisionArtifact(ctx, digest, object.Size)
}

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
