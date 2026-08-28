package pki

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	tokenSecretBytes = 32
	maxEpochAttempts = 8
)

type PublisherKey struct {
	ID        string
	PublicKey ed25519.PublicKey
}

type ServiceOptions struct {
	TokenLifetime       time.Duration
	IdempotencyLifetime time.Duration
	Random              io.Reader
}

type EnrollRequest struct {
	Token          string
	Name           string
	IdempotencyKey string
	Credential     CredentialRequest
}

type RecoverRequest struct {
	Token string
	// NodeID is optional for token-only recovery. When supplied, it must match
	// the node bound to the recovery token.
	NodeID         string
	IdempotencyKey string
	Credential     CredentialRequest
}

type RotateRequest struct {
	NodeID         string
	PreviousSerial string
	IdempotencyKey string
	Credential     CredentialRequest
}

type Service struct {
	repository          Repository
	issuer              CertificateIssuer
	sealer              ResponseSealer
	clock               Clock
	serials             SerialSource
	epochs              EpochSource
	random              io.Reader
	tokenLifetime       time.Duration
	idempotencyLifetime time.Duration
	publisher           PublisherKey
}

func NewService(repository Repository, issuer CertificateIssuer, sealer ResponseSealer, clock Clock, serials SerialSource, epochs EpochSource, publisher PublisherKey, options ServiceOptions) (*Service, error) {
	if repository == nil || issuer == nil || sealer == nil || clock == nil || serials == nil || epochs == nil {
		return nil, fmt.Errorf("%w: missing service dependency", ErrInvalidInput)
	}
	if publisher.ID == "" || len(publisher.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: invalid publisher key", ErrInvalidInput)
	}
	if options.TokenLifetime <= 0 || options.IdempotencyLifetime <= 0 {
		return nil, fmt.Errorf("%w: token and idempotency lifetimes must be positive", ErrInvalidInput)
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	publisher.PublicKey = clonePublicKey(publisher.PublicKey)
	return &Service{
		repository:          repository,
		issuer:              issuer,
		sealer:              sealer,
		clock:               clock,
		serials:             serials,
		epochs:              epochs,
		random:              options.Random,
		tokenLifetime:       options.TokenLifetime,
		idempotencyLifetime: options.IdempotencyLifetime,
		publisher:           publisher,
	}, nil
}

func (service *Service) CreateEnrollmentToken(ctx context.Context, mutation Mutation) (TokenSecret, error) {
	return service.createToken(ctx, mutation, PurposeEnroll, "")
}

func (service *Service) CreateRecoveryToken(ctx context.Context, mutation Mutation, nodeID string) (TokenSecret, error) {
	if err := validateNodeID(nodeID); err != nil {
		return TokenSecret{}, err
	}
	return service.createToken(ctx, mutation, PurposeRecover, nodeID)
}

func (service *Service) createToken(ctx context.Context, mutation Mutation, purpose TokenPurpose, nodeID string) (TokenSecret, error) {
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return TokenSecret{}, ErrInvalidMutation
	}
	operation := OperationCreateEnrollmentToken
	if purpose == PurposeRecover {
		operation = OperationCreateRecoveryToken
	}
	now := service.clock.Now().UTC()
	tokenExpiresAt := now.Add(service.tokenLifetime)
	idempotency := service.idempotency(operation, mutation.ActorID, mutation.IdempotencyKey, requestDigest(string(operation), string(purpose), nodeID), now)
	if idempotency.ExpiresAt.After(tokenExpiresAt) {
		idempotency.ExpiresAt = tokenExpiresAt
	}
	result, err := executeIdempotent(ctx, service.repository, service.sealer, idempotency, func(store Store) (TokenSecret, error) {
		if purpose == PurposeRecover {
			if _, err := store.Node(ctx, nodeID); err != nil {
				return TokenSecret{}, err
			}
		}
		secret, err := service.newTokenSecret()
		if err != nil {
			return TokenSecret{}, err
		}
		record := Token{
			Hash:      HashToken(secret),
			Purpose:   purpose,
			NodeID:    nodeID,
			ExpiresAt: tokenExpiresAt,
			CreatedBy: mutation.ActorID,
			CreatedAt: now,
		}
		created, err := store.CreateToken(ctx, record)
		if err != nil {
			return TokenSecret{}, err
		}
		if created.ID == "" || created.Hash != record.Hash || created.Purpose != purpose || created.NodeID != nodeID ||
			!created.ExpiresAt.Equal(record.ExpiresAt) {
			return TokenSecret{}, fmt.Errorf("repository returned inconsistent token: %w", ErrInvalidInput)
		}
		return TokenSecret{ID: created.ID, Secret: secret, ExpiresAt: record.ExpiresAt}, nil
	})
	if err != nil {
		return TokenSecret{}, err
	}
	if result.ID == "" || result.Secret == "" || result.ExpiresAt.IsZero() {
		return TokenSecret{}, ErrInvalidIdempotencyResult
	}
	return result, nil
}

func (service *Service) Enroll(ctx context.Context, request EnrollRequest) (Enrollment, error) {
	if request.Token == "" || request.IdempotencyKey == "" {
		return Enrollment{}, fmt.Errorf("%w: token and idempotency key are required", ErrInvalidInput)
	}
	publicKey, err := ParseCredentialRequest(request.Credential)
	if err != nil {
		return Enrollment{}, err
	}
	now := service.clock.Now().UTC()
	hash := requestDigest(string(OperationEnroll), string(HashToken(request.Token)), request.Name, string(request.Credential.PublicKey), string(request.Credential.CSR))
	idempotency := service.idempotency(OperationEnroll, "token:"+string(HashToken(request.Token)), request.IdempotencyKey, hash, now)
	result, err := executeIdempotent(ctx, service.repository, service.sealer, idempotency, func(store Store) (Enrollment, error) {
		if _, err := consumeExpected(ctx, store, request.Token, PurposeEnroll, "", now); err != nil {
			return Enrollment{}, err
		}
		epoch, err := service.nextEpoch(ctx, "")
		if err != nil {
			return Enrollment{}, err
		}
		node, err := store.CreateNode(ctx, request.Name, epoch, now)
		if err != nil {
			return Enrollment{}, err
		}
		if err := validateCreatedNode(node, epoch); err != nil {
			return Enrollment{}, err
		}
		credential, record, err := service.issue(ctx, node.ID, publicKey, now)
		if err != nil {
			return Enrollment{}, err
		}
		if err := store.CreateCredential(ctx, record); err != nil {
			return Enrollment{}, err
		}
		return service.enrollment(node.ID, epoch, credential), nil
	})
	if err != nil {
		return Enrollment{}, err
	}
	if err := service.validateEnrollmentResult(result); err != nil {
		return Enrollment{}, err
	}
	return result, nil
}

func (service *Service) Recover(ctx context.Context, request RecoverRequest) (Recovery, error) {
	if request.Token == "" || request.IdempotencyKey == "" {
		return Recovery{}, fmt.Errorf("%w: token and idempotency key are required", ErrInvalidInput)
	}
	if request.NodeID != "" {
		if err := validateNodeID(request.NodeID); err != nil {
			return Recovery{}, err
		}
	}
	publicKey, err := ParseCredentialRequest(request.Credential)
	if err != nil {
		return Recovery{}, err
	}
	now := service.clock.Now().UTC()
	hash := requestDigest(string(OperationRecover), string(HashToken(request.Token)), request.NodeID, string(request.Credential.PublicKey), string(request.Credential.CSR))
	idempotency := service.idempotency(OperationRecover, "token:"+string(HashToken(request.Token)), request.IdempotencyKey, hash, now)
	result, err := executeIdempotent(ctx, service.repository, service.sealer, idempotency, func(store Store) (Recovery, error) {
		token, err := consumeExpected(ctx, store, request.Token, PurposeRecover, request.NodeID, now)
		if err != nil {
			return Recovery{}, err
		}
		node, err := store.Node(ctx, token.NodeID)
		if err != nil {
			return Recovery{}, err
		}
		newEpoch, err := service.nextEpoch(ctx, node.EventEpoch)
		if err != nil {
			return Recovery{}, err
		}
		credential, record, err := service.issue(ctx, node.ID, publicKey, now)
		if err != nil {
			return Recovery{}, err
		}
		if _, err := store.RevokeNodeCredentials(ctx, node.ID, now); err != nil {
			return Recovery{}, err
		}
		previousEpoch := node.EventEpoch
		node.EventEpoch = newEpoch
		node.CredentialState = CredentialActive
		if err := store.UpdateNode(ctx, node); err != nil {
			return Recovery{}, err
		}
		if err := store.CreateCredential(ctx, record); err != nil {
			return Recovery{}, err
		}
		return Recovery{
			Enrollment: service.enrollment(node.ID, newEpoch, credential),
			Record: RecoveryRecord{
				NodeID:        node.ID,
				PreviousEpoch: previousEpoch,
				NewEpoch:      newEpoch,
				RecoveredAt:   now,
			},
		}, nil
	})
	if err != nil {
		return Recovery{}, err
	}
	if err := service.validateEnrollmentResult(result.Enrollment); err != nil {
		return Recovery{}, err
	}
	if result.Record.NodeID != result.NodeID || result.Record.NewEpoch != result.EventEpoch || result.Record.PreviousEpoch == result.Record.NewEpoch {
		return Recovery{}, ErrInvalidIdempotencyResult
	}
	return result, nil
}

func (service *Service) Rotate(ctx context.Context, request RotateRequest) (Rotation, error) {
	if err := validateNodeID(request.NodeID); err != nil {
		return Rotation{}, err
	}
	if request.PreviousSerial == "" || request.IdempotencyKey == "" {
		return Rotation{}, fmt.Errorf("%w: previous serial and idempotency key are required", ErrInvalidInput)
	}
	publicKey, err := ParseCredentialRequest(request.Credential)
	if err != nil {
		return Rotation{}, err
	}
	now := service.clock.Now().UTC()
	hash := requestDigest(string(OperationRotate), request.NodeID, request.PreviousSerial, string(request.Credential.PublicKey), string(request.Credential.CSR))
	idempotency := service.idempotency(OperationRotate, request.NodeID, request.IdempotencyKey, hash, now)
	result, err := executeIdempotent(ctx, service.repository, service.sealer, idempotency, func(store Store) (Rotation, error) {
		node, err := store.Node(ctx, request.NodeID)
		if err != nil {
			return Rotation{}, err
		}
		if node.CredentialState == CredentialRevoked {
			return Rotation{}, ErrNodeRevoked
		}
		previous, err := store.Credential(ctx, request.NodeID, request.PreviousSerial)
		if err != nil {
			return Rotation{}, err
		}
		if previous.RevokedAt != nil {
			return Rotation{}, ErrCredentialRevoked
		}
		credential, record, err := service.issue(ctx, node.ID, publicKey, now)
		if err != nil {
			return Rotation{}, err
		}
		if err := store.CreateCredential(ctx, record); err != nil {
			return Rotation{}, err
		}
		if _, err := store.RevokeCredential(ctx, node.ID, previous.Serial, now); err != nil {
			return Rotation{}, err
		}
		return Rotation{
			ClientCredential: credential,
			Record: RotationRecord{
				NodeID:         node.ID,
				PreviousSerial: previous.Serial,
				NewSerial:      credential.Serial,
				RotatedAt:      now,
			},
		}, nil
	})
	if err != nil {
		return Rotation{}, err
	}
	if result.Certificate == "" || result.Serial == "" || result.Record.NodeID != request.NodeID ||
		result.Record.PreviousSerial != request.PreviousSerial || result.Record.NewSerial != result.Serial {
		return Rotation{}, ErrInvalidIdempotencyResult
	}
	return result, nil
}

func (service *Service) Revoke(ctx context.Context, mutation Mutation, nodeID, serial string) (RevocationRecord, error) {
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return RevocationRecord{}, ErrInvalidMutation
	}
	if err := validateNodeID(nodeID); err != nil {
		return RevocationRecord{}, err
	}
	if serial == "" {
		return RevocationRecord{}, fmt.Errorf("%w: serial is required", ErrInvalidInput)
	}
	now := service.clock.Now().UTC()
	hash := requestDigest(string(OperationRevoke), nodeID, serial)
	idempotency := service.idempotency(OperationRevoke, mutation.ActorID, mutation.IdempotencyKey, hash, now)
	result, err := executeIdempotent(ctx, service.repository, service.sealer, idempotency, func(store Store) (RevocationRecord, error) {
		credential, err := store.Credential(ctx, nodeID, serial)
		if err != nil {
			return RevocationRecord{}, err
		}
		if credential.RevokedAt != nil {
			return RevocationRecord{}, ErrCredentialRevoked
		}
		revoked, err := store.RevokeCredential(ctx, nodeID, serial, now)
		if err != nil {
			return RevocationRecord{}, err
		}
		if revoked.RevokedAt == nil {
			return RevocationRecord{}, fmt.Errorf("repository did not record revocation: %w", ErrInvalidInput)
		}
		active, err := store.HasActiveCredential(ctx, nodeID, now)
		if err != nil {
			return RevocationRecord{}, err
		}
		if !active {
			node, err := store.Node(ctx, nodeID)
			if err != nil {
				return RevocationRecord{}, err
			}
			node.CredentialState = CredentialRevoked
			if err := store.UpdateNode(ctx, node); err != nil {
				return RevocationRecord{}, err
			}
		}
		return RevocationRecord{NodeID: nodeID, Serial: serial, RevokedAt: *revoked.RevokedAt}, nil
	})
	if err != nil {
		return RevocationRecord{}, err
	}
	if result.NodeID != nodeID || result.Serial != serial || result.RevokedAt.IsZero() {
		return RevocationRecord{}, ErrInvalidIdempotencyResult
	}
	return result, nil
}

func (service *Service) RevokeNode(ctx context.Context, mutation Mutation, nodeID string) (NodeRevocationRecord, error) {
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return NodeRevocationRecord{}, ErrInvalidMutation
	}
	if err := validateNodeID(nodeID); err != nil {
		return NodeRevocationRecord{}, err
	}
	now := service.clock.Now().UTC()
	hash := requestDigest(string(OperationRevokeNode), nodeID)
	idempotency := service.idempotency(OperationRevokeNode, mutation.ActorID, mutation.IdempotencyKey, hash, now)
	result, err := executeIdempotent(ctx, service.repository, service.sealer, idempotency, func(store Store) (NodeRevocationRecord, error) {
		node, err := store.Node(ctx, nodeID)
		if err != nil {
			return NodeRevocationRecord{}, err
		}
		credentials, err := store.RevokeNodeCredentials(ctx, nodeID, now)
		if err != nil {
			return NodeRevocationRecord{}, err
		}
		node.CredentialState = CredentialRevoked
		if err := store.UpdateNode(ctx, node); err != nil {
			return NodeRevocationRecord{}, err
		}
		return NodeRevocationRecord{NodeID: nodeID, RevokedCredentials: len(credentials), RevokedAt: now}, nil
	})
	if err != nil {
		return NodeRevocationRecord{}, err
	}
	if result.NodeID != nodeID || result.RevokedAt.IsZero() {
		return NodeRevocationRecord{}, ErrInvalidIdempotencyResult
	}
	return result, nil
}

func (service *Service) issue(ctx context.Context, nodeID string, publicKey crypto.PublicKey, now time.Time) (ClientCredential, Credential, error) {
	serial, err := service.serials.NextSerial(ctx)
	if err != nil {
		return ClientCredential{}, Credential{}, fmt.Errorf("generate certificate serial: %w", err)
	}
	credential, err := service.issuer.Issue(nodeID, publicKey, serial, now)
	if err != nil {
		return ClientCredential{}, Credential{}, err
	}
	if credential.Serial != serial.String() || !credential.ExpiresAt.After(now) || credential.Certificate == "" {
		return ClientCredential{}, Credential{}, fmt.Errorf("issuer returned inconsistent credential: %w", ErrInvalidInput)
	}
	return credential, Credential{
		NodeID:    nodeID,
		Serial:    credential.Serial,
		NotAfter:  credential.ExpiresAt,
		CreatedAt: now,
	}, nil
}

func (service *Service) enrollment(nodeID, epoch string, credential ClientCredential) Enrollment {
	return Enrollment{
		ClientCredential:   credential,
		NodeID:             nodeID,
		EventEpoch:         epoch,
		PublisherKeyID:     service.publisher.ID,
		PublisherPublicKey: clonePublicKey(service.publisher.PublicKey),
	}
}

func (service *Service) validateEnrollmentResult(result Enrollment) error {
	if result.NodeID == "" || !validUUID(result.EventEpoch) || result.Certificate == "" || result.Serial == "" ||
		result.ExpiresAt.IsZero() || result.PublisherKeyID != service.publisher.ID ||
		!service.publisher.PublicKey.Equal(result.PublisherPublicKey) {
		return ErrInvalidIdempotencyResult
	}
	return nil
}

func (service *Service) idempotency(operation IdempotencyOperation, actorID, key, hash string, now time.Time) IdempotencyRequest {
	return IdempotencyRequest{
		Operation:   operation,
		ActorID:     actorID,
		Key:         key,
		RequestHash: hash,
		CreatedAt:   now,
		ExpiresAt:   now.Add(service.idempotencyLifetime),
	}
}

func executeIdempotent[T any](ctx context.Context, repository Repository, sealer ResponseSealer, request IdempotencyRequest, fn func(Store) (T, error)) (T, error) {
	var zero T
	additionalData := idempotencyAdditionalData(request)
	sealed, err := repository.IdempotentTransaction(ctx, request, func(store Store) ([]byte, error) {
		result, err := fn(store)
		if err != nil {
			return nil, err
		}
		response, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("encode idempotency result: %w", err)
		}
		sealed, err := sealer.Seal(response, additionalData)
		if err != nil {
			return nil, err
		}
		return sealed, nil
	})
	if err != nil {
		return zero, err
	}
	response, err := sealer.Open(sealed, additionalData)
	if err != nil {
		return zero, err
	}
	if len(response) == 0 {
		return zero, ErrInvalidIdempotencyResult
	}
	var result T
	if err := json.Unmarshal(response, &result); err != nil {
		return zero, fmt.Errorf("%w: %v", ErrInvalidIdempotencyResult, err)
	}
	return result, nil
}

func idempotencyAdditionalData(request IdempotencyRequest) []byte {
	return []byte(requestDigest(string(request.Operation), request.ActorID, request.Key, request.RequestHash))
}

func requestDigest(parts ...string) string {
	hash := sha256.New()
	var size [8]byte
	for _, part := range parts {
		binary.BigEndian.PutUint64(size[:], uint64(len(part)))
		_, _ = hash.Write(size[:])
		_, _ = io.WriteString(hash, part)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (service *Service) newTokenSecret() (string, error) {
	value := make([]byte, tokenSecretBytes)
	if _, err := io.ReadFull(service.random, value); err != nil {
		return "", fmt.Errorf("generate token secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (service *Service) nextEpoch(ctx context.Context, previous string) (string, error) {
	for range maxEpochAttempts {
		epoch, err := service.epochs.NextEpoch(ctx)
		if err != nil {
			return "", fmt.Errorf("generate event epoch: %w", err)
		}
		if !validUUID(epoch) {
			return "", fmt.Errorf("%w: epoch source returned a non-UUID", ErrInvalidInput)
		}
		if epoch != previous {
			return epoch, nil
		}
	}
	return "", ErrEpochUnchanged
}

func consumeExpected(ctx context.Context, store Store, secret string, purpose TokenPurpose, expectedNodeID string, now time.Time) (Token, error) {
	token, err := store.ConsumeToken(ctx, HashToken(secret), now)
	if err != nil {
		return Token{}, err
	}
	if token.Purpose != purpose {
		return Token{}, ErrWrongTokenPurpose
	}
	if !token.ExpiresAt.After(now) {
		return Token{}, ErrTokenExpired
	}
	if purpose == PurposeEnroll && token.NodeID != "" {
		return Token{}, ErrWrongNode
	}
	if purpose == PurposeRecover {
		if token.NodeID == "" || expectedNodeID != "" && token.NodeID != expectedNodeID {
			return Token{}, ErrWrongNode
		}
	}
	return token, nil
}

func validateCreatedNode(node Node, epoch string) error {
	if err := validateNodeID(node.ID); err != nil {
		return fmt.Errorf("repository returned invalid node: %w", err)
	}
	if node.EventEpoch != epoch || node.CredentialState != CredentialActive {
		return fmt.Errorf("repository returned inconsistent node: %w", ErrInvalidInput)
	}
	return nil
}

func clonePublicKey(key ed25519.PublicKey) ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), key...)
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' ||
		value[14] != '4' || !strings.ContainsRune("89abAB", rune(value[19])) {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}
