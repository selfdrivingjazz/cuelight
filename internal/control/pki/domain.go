package pki

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"time"
)

var (
	ErrInvalidInput             = errors.New("invalid PKI input")
	ErrInvalidMutation          = errors.New("invalid mutation context")
	ErrInvalidPublicKey         = errors.New("invalid public key")
	ErrInvalidCSR               = errors.New("invalid certificate signing request")
	ErrInvalidCA                = errors.New("invalid certificate authority")
	ErrInvalidSerial            = errors.New("invalid certificate serial")
	ErrTokenNotFound            = errors.New("token not found")
	ErrTokenConsumed            = errors.New("token already consumed")
	ErrTokenExpired             = errors.New("token expired")
	ErrWrongTokenPurpose        = errors.New("wrong token purpose")
	ErrWrongNode                = errors.New("token is bound to a different node")
	ErrNodeNotFound             = errors.New("node not found")
	ErrNodeRevoked              = errors.New("node credentials are revoked")
	ErrCredentialNotFound       = errors.New("credential not found")
	ErrCredentialRevoked        = errors.New("credential already revoked")
	ErrEpochUnchanged           = errors.New("event epoch did not change")
	ErrIdempotencyConflict      = errors.New("idempotency key reused with a different request")
	ErrInvalidIdempotencyResult = errors.New("invalid cached idempotency result")
	ErrInvalidSealedResponse    = errors.New("invalid sealed idempotency response")
)

type TokenPurpose string

const (
	PurposeEnroll  TokenPurpose = "enroll"
	PurposeRecover TokenPurpose = "recover"
)

type IdempotencyOperation string

const (
	OperationCreateEnrollmentToken IdempotencyOperation = "pki.create-enrollment-token"
	OperationCreateRecoveryToken   IdempotencyOperation = "pki.create-recovery-token"
	OperationEnroll                IdempotencyOperation = "pki.enroll"
	OperationRecover               IdempotencyOperation = "pki.recover"
	OperationRotate                IdempotencyOperation = "pki.rotate"
	OperationRevoke                IdempotencyOperation = "pki.revoke"
)

type CredentialState string

const (
	CredentialActive  CredentialState = "active"
	CredentialRevoked CredentialState = "revoked"
	CredentialExpired CredentialState = "expired"
)

type TokenHash string

type Mutation struct {
	ActorID        string
	IdempotencyKey string
}

type Token struct {
	ID         string
	Hash       TokenHash
	Purpose    TokenPurpose
	NodeID     string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedBy  string
	CreatedAt  time.Time
}

type TokenSecret struct {
	ID        string    `json:"id"`
	Secret    string    `json:"secret"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Node struct {
	ID              string
	Name            string
	EventEpoch      string
	CredentialState CredentialState
	CreatedAt       time.Time
}

type Credential struct {
	NodeID    string
	Serial    string
	NotAfter  time.Time
	CreatedAt time.Time
	RevokedAt *time.Time
}

type CredentialRequest struct {
	// PublicKey is either a PKIX DER/PEM public key or a raw Ed25519 public key.
	PublicKey []byte
	// CSR is a DER/PEM PKCS#10 request. Exactly one of PublicKey and CSR is required.
	CSR []byte
}

type ClientCredential struct {
	Certificate string    `json:"certificate"`
	Serial      string    `json:"serial"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type Enrollment struct {
	ClientCredential
	NodeID             string            `json:"nodeId"`
	EventEpoch         string            `json:"eventEpoch"`
	PublisherKeyID     string            `json:"publisherKeyId"`
	PublisherPublicKey ed25519.PublicKey `json:"publisherPublicKey"`
}

type RotationRecord struct {
	NodeID         string    `json:"nodeId"`
	PreviousSerial string    `json:"previousSerial"`
	NewSerial      string    `json:"newSerial"`
	RotatedAt      time.Time `json:"rotatedAt"`
}

type RevocationRecord struct {
	NodeID    string    `json:"nodeId"`
	Serial    string    `json:"serial"`
	RevokedAt time.Time `json:"revokedAt"`
}

type RecoveryRecord struct {
	NodeID        string    `json:"nodeId"`
	PreviousEpoch string    `json:"previousEpoch"`
	NewEpoch      string    `json:"newEpoch"`
	RecoveredAt   time.Time `json:"recoveredAt"`
}

type Recovery struct {
	Enrollment
	Record RecoveryRecord `json:"recovery"`
}

type Rotation struct {
	ClientCredential
	Record RotationRecord `json:"rotation"`
}

type Clock interface {
	Now() time.Time
}

type SerialSource interface {
	NextSerial(context.Context) (*big.Int, error)
}

type EpochSource interface {
	NextEpoch(context.Context) (string, error)
}

type CertificateIssuer interface {
	Issue(nodeID string, publicKey crypto.PublicKey, serial *big.Int, now time.Time) (ClientCredential, error)
}

// ResponseSealer protects cached idempotency responses at rest. Additional
// data binds ciphertext to its operation, actor, key, and request digest.
type ResponseSealer interface {
	Seal(plaintext, additionalData []byte) ([]byte, error)
	Open(sealed, additionalData []byte) ([]byte, error)
}

type IdempotencyRequest struct {
	Operation   IdempotencyOperation
	ActorID     string
	Key         string
	RequestHash string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Repository runs fn and persists its exact opaque response atomically with
// every Store mutation. A retry with the same operation, actor, key, and
// request hash returns the persisted response without invoking fn. Reusing the
// key with a different request hash returns ErrIdempotencyConflict.
type Repository interface {
	IdempotentTransaction(context.Context, IdempotencyRequest, func(Store) ([]byte, error)) ([]byte, error)
}

// Store is the persistence boundary used inside a Repository transaction.
// CreateToken and CreateNode assign their record IDs.
type Store interface {
	CreateToken(context.Context, Token) (Token, error)
	ConsumeToken(context.Context, TokenHash, time.Time) (Token, error)

	CreateNode(context.Context, string, string, time.Time) (Node, error)
	Node(context.Context, string) (Node, error)
	UpdateNode(context.Context, Node) error

	CreateCredential(context.Context, Credential) error
	Credential(context.Context, string, string) (Credential, error)
	HasActiveCredential(context.Context, string, time.Time) (bool, error)
	RevokeCredential(context.Context, string, string, time.Time) (Credential, error)
	RevokeNodeCredentials(context.Context, string, time.Time) ([]Credential, error)
}

func HashToken(secret string) TokenHash {
	sum := sha256.Sum256([]byte(secret))
	return TokenHash(hex.EncodeToString(sum[:]))
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func SystemClock() Clock { return systemClock{} }
