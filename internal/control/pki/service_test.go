package pki

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/protocol"
)

func TestEnrollmentTokenAndResultReplayAtomically(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	mutation := Mutation{ActorID: "actor-1", IdempotencyKey: "token-1"}
	token, err := environment.service.CreateEnrollmentToken(ctx, mutation)
	if err != nil {
		t.Fatal(err)
	}
	replayedToken, err := environment.service.CreateEnrollmentToken(ctx, mutation)
	if err != nil {
		t.Fatal(err)
	}
	if replayedToken != token || len(environment.repository.tokens) != 1 {
		t.Fatalf("token replay created a new secret: first=%+v replay=%+v records=%d", token, replayedToken, len(environment.repository.tokens))
	}
	stored, exists := environment.repository.tokens[HashToken(token.Secret)]
	if !exists {
		t.Fatal("token hash was not persisted")
	}
	if stored.CreatedBy != "actor-1" || stored.Purpose != PurposeEnroll {
		t.Fatalf("unexpected stored token: %+v", stored)
	}
	if len(environment.repository.idempotency) != 1 {
		t.Fatalf("idempotency records = %d, want 1", len(environment.repository.idempotency))
	}
	for _, cached := range environment.repository.idempotency {
		if bytes.Contains(cached.response, []byte(token.Secret)) {
			t.Fatal("cached idempotency response contains plaintext token secret")
		}
		if !cached.expiresAt.Equal(token.ExpiresAt) {
			t.Fatalf("token replay cache expires at %v, want token expiry %v", cached.expiresAt, token.ExpiresAt)
		}
	}
	request := EnrollRequest{
		Token:          token.Secret,
		IdempotencyKey: "enrollment-1",
		Credential:     CredentialRequest{PublicKey: newPublicKey(t)},
	}
	enrollment, err := environment.service.Enroll(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := environment.service.Enroll(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.NodeID != enrollment.NodeID || replayed.EventEpoch != enrollment.EventEpoch ||
		replayed.Certificate != enrollment.Certificate || replayed.Serial != enrollment.Serial || len(environment.repository.nodes) != 1 {
		t.Fatalf("enrollment replay changed result: first=%+v replay=%+v", enrollment, replayed)
	}
	conflict := request
	conflict.Name = "different-request"
	if _, err := environment.service.Enroll(ctx, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed request under same key error = %v, want ErrIdempotencyConflict", err)
	}
	request.IdempotencyKey = "enrollment-2"
	if _, err := environment.service.Enroll(ctx, request); !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("token reuse under new key error = %v, want ErrTokenConsumed", err)
	}
}

func TestExpiredTokenSecretResponseIsUnavailable(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	mutation := Mutation{ActorID: "actor-1", IdempotencyKey: "expired-secret"}
	token, err := environment.service.CreateEnrollmentToken(ctx, mutation)
	if err != nil {
		t.Fatal(err)
	}
	environment.clock.now = token.ExpiresAt
	replacement, err := environment.service.CreateEnrollmentToken(ctx, mutation)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Secret == token.Secret || replacement.ID == token.ID || !replacement.ExpiresAt.After(token.ExpiresAt) {
		t.Fatalf("expired response was replayed: expired=%+v replacement=%+v", token, replacement)
	}
	if len(environment.repository.tokens) != 2 {
		t.Fatalf("token records = %d, want expired original and replacement", len(environment.repository.tokens))
	}
	for _, cached := range environment.repository.idempotency {
		if bytes.Contains(cached.response, []byte(token.Secret)) {
			t.Fatal("expired plaintext secret remains in idempotency cache")
		}
	}
}

func TestEnrollmentRejectsExpiredTokenWithoutChangingItToReplay(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	token, err := environment.service.CreateEnrollmentToken(ctx, Mutation{ActorID: "actor-1", IdempotencyKey: "token-expiry"})
	if err != nil {
		t.Fatal(err)
	}
	environment.clock.now = token.ExpiresAt
	request := EnrollRequest{Token: token.Secret, IdempotencyKey: "expired-enrollment", Credential: CredentialRequest{PublicKey: newPublicKey(t)}}
	for attempt := range 2 {
		if _, err := environment.service.Enroll(ctx, request); !errors.Is(err, ErrTokenExpired) {
			t.Fatalf("attempt %d error = %v, want ErrTokenExpired", attempt+1, err)
		}
	}
}

func TestTokensRejectWrongPurposeAndNodeWithoutBeingConsumed(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	enrollment, _ := enrollNode(t, environment)
	token, err := environment.service.CreateRecoveryToken(ctx, Mutation{ActorID: "actor-1", IdempotencyKey: "recovery-token"}, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	key := newPublicKey(t)
	if _, err := environment.service.Enroll(ctx, EnrollRequest{
		Token: token.Secret, IdempotencyKey: "wrong-purpose", Credential: CredentialRequest{PublicKey: key},
	}); !errors.Is(err, ErrWrongTokenPurpose) {
		t.Fatalf("wrong-purpose error = %v, want ErrWrongTokenPurpose", err)
	}
	if _, err := environment.service.Recover(ctx, RecoverRequest{
		Token: token.Secret, NodeID: "wrong-node", IdempotencyKey: "wrong-node", Credential: CredentialRequest{PublicKey: key},
	}); !errors.Is(err, ErrWrongNode) {
		t.Fatalf("wrong-node error = %v, want ErrWrongNode", err)
	}
	if _, err := environment.service.Recover(ctx, RecoverRequest{
		Token: token.Secret, NodeID: enrollment.NodeID, IdempotencyKey: "valid-recovery", Credential: CredentialRequest{PublicKey: key},
	}); err != nil {
		t.Fatalf("token was consumed by rejected attempt: %v", err)
	}
}

func TestCredentialRequestRejectsInvalidKeyAndCSR(t *testing.T) {
	if _, err := ParseCredentialRequest(CredentialRequest{PublicKey: []byte("not a public key")}); !errors.Is(err, ErrInvalidPublicKey) {
		t.Fatalf("invalid-key error = %v, want ErrInvalidPublicKey", err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "node"}}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	csr[len(csr)-1] ^= 0xff
	if _, err := ParseCredentialRequest(CredentialRequest{CSR: csr}); !errors.Is(err, ErrInvalidCSR) {
		t.Fatalf("invalid-CSR error = %v, want ErrInvalidCSR", err)
	}
	if _, err := ParseCredentialRequest(CredentialRequest{PublicKey: newPublicKey(t), CSR: csr}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("ambiguous request error = %v, want ErrInvalidInput", err)
	}
}

func TestIssuedCertificateBindsOnlyNodeSANAndClientAuthEKU(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "untrusted subject"},
		DNSNames: []string{"attacker.invalid"},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	token, err := environment.service.CreateEnrollmentToken(ctx, Mutation{ActorID: "actor-1", IdempotencyKey: "san-token"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := environment.service.Enroll(ctx, EnrollRequest{
		Token: token.Secret, IdempotencyKey: "san-enrollment", Credential: CredentialRequest{CSR: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})},
	})
	if err != nil {
		t.Fatal(err)
	}
	certificate := parseCertificate(t, enrollment.Certificate)
	wantSAN := protocol.NodeSANPrefix + enrollment.NodeID
	if len(certificate.DNSNames) != 1 || certificate.DNSNames[0] != wantSAN {
		t.Fatalf("certificate DNS SANs = %v, want [%q]", certificate.DNSNames, wantSAN)
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("certificate EKUs = %v, want client auth only", certificate.ExtKeyUsage)
	}
	if certificate.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Fatalf("certificate key usage = %v, want digital signature", certificate.KeyUsage)
	}
	if err := certificate.CheckSignatureFrom(environment.ca); err != nil {
		t.Fatalf("certificate is not signed by CA: %v", err)
	}
	certificatePublicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(certificatePublicKey, publicKey) {
		t.Fatal("certificate does not carry requested public key")
	}
	if enrollment.PublisherKeyID != "publisher-1" || !bytes.Equal(enrollment.PublisherPublicKey, environment.publisher) {
		t.Fatal("enrollment did not carry publisher trust key")
	}
}

func TestRotationPreservesNodeIdentityAndRevokesPreviousCredential(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	enrollment, _ := enrollNode(t, environment)
	nodeBefore := environment.repository.nodes[enrollment.NodeID]
	rotationRequest := RotateRequest{
		NodeID:         enrollment.NodeID,
		PreviousSerial: enrollment.Serial,
		IdempotencyKey: "rotation-1",
		Credential:     CredentialRequest{PublicKey: newPublicKey(t)},
	}
	rotation, err := environment.service.Rotate(ctx, rotationRequest)
	if err != nil {
		t.Fatal(err)
	}
	replayedRotation, err := environment.service.Rotate(ctx, rotationRequest)
	if err != nil {
		t.Fatal(err)
	}
	if replayedRotation.Certificate != rotation.Certificate || replayedRotation.Record != rotation.Record {
		t.Fatalf("rotation replay changed result: first=%+v replay=%+v", rotation, replayedRotation)
	}
	if rotation.Record.NodeID != enrollment.NodeID || rotation.Record.PreviousSerial != enrollment.Serial || rotation.Record.NewSerial != rotation.Serial {
		t.Fatalf("unexpected rotation record: %+v", rotation.Record)
	}
	certificate := parseCertificate(t, rotation.Certificate)
	if len(certificate.DNSNames) != 1 || certificate.DNSNames[0] != protocol.NodeSANPrefix+enrollment.NodeID {
		t.Fatalf("rotated SANs = %v", certificate.DNSNames)
	}
	nodeAfter := environment.repository.nodes[enrollment.NodeID]
	if nodeAfter.ID != nodeBefore.ID || nodeAfter.EventEpoch != nodeBefore.EventEpoch {
		t.Fatalf("rotation changed node identity: before=%+v after=%+v", nodeBefore, nodeAfter)
	}
	previous := environment.repository.creds[credentialKey(enrollment.NodeID, enrollment.Serial)]
	current := environment.repository.creds[credentialKey(enrollment.NodeID, rotation.Serial)]
	if previous.RevokedAt == nil || current.RevokedAt != nil {
		t.Fatalf("rotation revocation state: previous=%+v current=%+v", previous, current)
	}
}

func TestRevocationRecordsCredentialAndRejectsRepeat(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	enrollment, _ := enrollNode(t, environment)
	mutation := Mutation{ActorID: "security-admin", IdempotencyKey: "revoke-1"}
	record, err := environment.service.Revoke(ctx, mutation, enrollment.NodeID, enrollment.Serial)
	if err != nil {
		t.Fatal(err)
	}
	if record.NodeID != enrollment.NodeID || record.Serial != enrollment.Serial || !record.RevokedAt.Equal(environment.clock.now) {
		t.Fatalf("unexpected revocation record: %+v", record)
	}
	credential := environment.repository.creds[credentialKey(enrollment.NodeID, enrollment.Serial)]
	if credential.RevokedAt == nil || !credential.RevokedAt.Equal(record.RevokedAt) {
		t.Fatalf("credential revocation was not persisted: %+v", credential)
	}
	if state := environment.repository.nodes[enrollment.NodeID].CredentialState; state != CredentialRevoked {
		t.Fatalf("node credential state = %q, want %q", state, CredentialRevoked)
	}
	replayed, err := environment.service.Revoke(ctx, mutation, enrollment.NodeID, enrollment.Serial)
	if err != nil || replayed != record {
		t.Fatalf("revocation replay = %+v, %v; want %+v", replayed, err, record)
	}
	mutation.IdempotencyKey = "revoke-2"
	if _, err := environment.service.Revoke(ctx, mutation, enrollment.NodeID, enrollment.Serial); !errors.Is(err, ErrCredentialRevoked) {
		t.Fatalf("repeat revocation under new key error = %v, want ErrCredentialRevoked", err)
	}
}

func TestRevokeNodeRevokesEveryCredentialAndReplays(t *testing.T) {
	environment := newTestEnvironment(t)
	enrollment, _ := enrollNode(t, environment)
	mutation := Mutation{ActorID: "security-admin", IdempotencyKey: "revoke-node-1"}
	record, err := environment.service.RevokeNode(context.Background(), mutation, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if record.NodeID != enrollment.NodeID || record.RevokedCredentials != 1 || record.RevokedAt.IsZero() {
		t.Fatalf("node revocation = %#v", record)
	}
	if state := environment.repository.nodes[enrollment.NodeID].CredentialState; state != CredentialRevoked {
		t.Fatalf("node credential state = %q", state)
	}
	replayed, err := environment.service.RevokeNode(context.Background(), mutation, enrollment.NodeID)
	if err != nil || replayed != record {
		t.Fatalf("replayed node revocation = %#v, %v", replayed, err)
	}
}

func TestRecoveryPreservesNodeAndCreatesNewEpoch(t *testing.T) {
	environment := newTestEnvironment(t)
	ctx := context.Background()
	enrollment, _ := enrollNode(t, environment)
	tokenMutation := Mutation{ActorID: "security-admin", IdempotencyKey: "recover-token-1"}
	token, err := environment.service.CreateRecoveryToken(ctx, tokenMutation, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	replayedToken, err := environment.service.CreateRecoveryToken(ctx, tokenMutation, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if replayedToken != token {
		t.Fatalf("recovery token replay changed secret: first=%+v replay=%+v", token, replayedToken)
	}
	recoveryRequest := RecoverRequest{
		Token:          token.Secret,
		IdempotencyKey: "recovery-1",
		Credential:     CredentialRequest{PublicKey: newPublicKey(t)},
	}
	recovery, err := environment.service.Recover(ctx, recoveryRequest)
	if err != nil {
		t.Fatal(err)
	}
	replayedRecovery, err := environment.service.Recover(ctx, recoveryRequest)
	if err != nil {
		t.Fatal(err)
	}
	if replayedRecovery.Certificate != recovery.Certificate || replayedRecovery.EventEpoch != recovery.EventEpoch ||
		replayedRecovery.Record != recovery.Record {
		t.Fatalf("recovery replay changed result: first=%+v replay=%+v", recovery, replayedRecovery)
	}
	if recovery.NodeID != enrollment.NodeID {
		t.Fatalf("recovery node ID = %q, want %q", recovery.NodeID, enrollment.NodeID)
	}
	if recovery.EventEpoch == enrollment.EventEpoch || recovery.Record.PreviousEpoch != enrollment.EventEpoch || recovery.Record.NewEpoch != recovery.EventEpoch {
		t.Fatalf("recovery did not replace epoch: enrollment=%q recovery=%+v", enrollment.EventEpoch, recovery)
	}
	if environment.repository.nodes[enrollment.NodeID].EventEpoch != recovery.EventEpoch {
		t.Fatal("new recovery epoch was not persisted")
	}
	oldCredential := environment.repository.creds[credentialKey(enrollment.NodeID, enrollment.Serial)]
	newCredential := environment.repository.creds[credentialKey(enrollment.NodeID, recovery.Serial)]
	if oldCredential.RevokedAt == nil || newCredential.RevokedAt != nil {
		t.Fatalf("recovery credential state: old=%+v new=%+v", oldCredential, newCredential)
	}
	recoveryRequest.IdempotencyKey = "recovery-2"
	if _, err := environment.service.Recover(ctx, recoveryRequest); !errors.Is(err, ErrTokenConsumed) {
		t.Fatalf("recovery under new key error = %v, want ErrTokenConsumed", err)
	}
}

func parseCertificate(t *testing.T, value string) *x509.Certificate {
	t.Helper()
	block, rest := pem.Decode([]byte(value))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		t.Fatal("invalid certificate PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func TestCertificateExpiresWithinConfiguredLifetime(t *testing.T) {
	environment := newTestEnvironment(t)
	enrollment, _ := enrollNode(t, environment)
	if got, want := enrollment.ExpiresAt, environment.clock.now.Add(24*time.Hour); !got.Equal(want) {
		t.Fatalf("expires at = %v, want %v", got, want)
	}
}
