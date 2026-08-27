package client

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestServerTrustAnchorValidation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	trusted := newTestClient(t, server, nil)
	defer trusted.CloseIdleConnections()
	if err := trusted.PutCapabilities(context.Background(), CapabilityReport{}, ""); err != nil {
		t.Fatalf("request with configured trust anchor: %v", err)
	}

	_, unrelatedCAPEM := makeCertificate(t, true)
	untrusted, err := New(Config{BaseURL: server.URL, ServerCAPEM: unrelatedCAPEM})
	if err != nil {
		t.Fatalf("construct client with unrelated trust anchor: %v", err)
	}
	defer untrusted.CloseIdleConnections()
	err = untrusted.PutCapabilities(context.Background(), CapabilityReport{}, "")
	var requestError *RequestError
	if !errors.As(err, &requestError) {
		t.Fatalf("untrusted server error = %T, want *RequestError", err)
	}
}

func TestNodeCertificateIsPresented(t *testing.T) {
	presented := make(chan string, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) != 1 {
			t.Errorf("peer certificates = %d, want 1", len(r.TLS.PeerCertificates))
		} else {
			presented <- r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	defer server.Close()

	nodeCertificate, _ := makeCertificate(t, false)
	client := newTestClient(t, server, &nodeCertificate)
	defer client.CloseIdleConnections()
	if err := client.PutCapabilities(context.Background(), CapabilityReport{}, ""); err != nil {
		t.Fatalf("request with node certificate: %v", err)
	}
	select {
	case commonName := <-presented:
		if commonName != "test-node" {
			t.Fatalf("client certificate common name = %q", commonName)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not observe client certificate")
	}
}

func TestConditionalAssignmentNotModified(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"generation-8"` {
			t.Errorf("If-None-Match = %q", got)
		}
		if got := r.URL.Query().Get("waitSeconds"); got != "12" {
			t.Errorf("waitSeconds = %q", got)
		}
		w.Header().Set("ETag", `"generation-8"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	result, err := client.GetAssignment(context.Background(), `"generation-8"`, 12*time.Second)
	if err != nil {
		t.Fatalf("get assignment: %v", err)
	}
	if !result.NotModified || result.ETag != `"generation-8"` {
		t.Fatalf("result = %+v", result)
	}
}

func TestLongPollCancellation(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.GetAssignment(ctx, "", 30*time.Second)
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("long poll error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("long poll did not stop after cancellation")
	}
}

func TestArtifactHeadAndRangedGet(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/player/artifacts/"+testDigest+"/content" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("ETag", `"artifact-1"`)
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "10")
			return
		}
		if got := r.Header.Get("Range"); got != "bytes=4-7" {
			t.Errorf("Range = %q", got)
		}
		if got := r.Header.Get("If-Range"); got != `"artifact-1"` {
			t.Errorf("If-Range = %q", got)
		}
		w.Header().Set("Content-Range", "bytes 4-7/10")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "4567")
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	metadata, err := client.HeadArtifact(context.Background(), testDigest)
	if err != nil {
		t.Fatalf("head artifact: %v", err)
	}
	if metadata.Size != 10 || metadata.ETag != `"artifact-1"` || metadata.AcceptRanges != "bytes" {
		t.Fatalf("metadata = %+v", metadata)
	}
	end := int64(7)
	content, err := client.GetArtifact(context.Background(), testDigest, &ByteRange{Start: 4, End: &end}, `"artifact-1"`)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	defer content.Body.Close()
	body, err := io.ReadAll(content.Body)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(body) != "4567" || !content.Partial || content.ContentRange != "bytes 4-7/10" {
		t.Fatalf("content = %+v, body = %q", content, body)
	}
}

func TestProblemParsingAndRedactedErrors(t *testing.T) {
	const secret = "enrollment-secret-that-must-not-leak"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request EnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode enrollment request: %v", err)
		}
		if request.Token != secret {
			t.Errorf("token = %q", request.Token)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(Problem{
			Type:          "urn:cable:invalid-token",
			Title:         "rejected " + secret,
			Status:        http.StatusUnauthorized,
			Detail:        "the supplied token was " + secret,
			CorrelationID: "correlation-1",
		})
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	_, err := client.Enroll(context.Background(), EnrollmentRequest{Token: secret, PublicKey: "public-key"}, "0123456789abcdef")
	var problemError *ProblemError
	if !errors.As(err, &problemError) {
		t.Fatalf("error = %T, want *ProblemError", err)
	}
	if problemError.Problem.Detail != "the supplied token was "+secret || problemError.Problem.CorrelationID != "correlation-1" {
		t.Fatalf("parsed problem = %+v", problemError.Problem)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), problemError.Problem.Title) {
		t.Fatalf("error string leaked server data: %q", err)
	}
}

func TestReportAndEventRequests(t *testing.T) {
	const idempotencyKey = "idempotency-key-1234"
	requests := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Idempotency-Key"); got != idempotencyKey {
			t.Errorf("Idempotency-Key = %q", got)
		}
		switch r.URL.Path {
		case "/player/reports":
			var report NodeReport
			if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
				t.Errorf("decode report: %v", err)
			}
			if report.EventEpoch != "epoch-1" || report.Sequence != 7 || report.ObservedGeneration != 3 {
				t.Errorf("report = %+v", report)
			}
			requests <- r.URL.Path
			w.WriteHeader(http.StatusAccepted)
		case "/player/events:append":
			var batch EventBatch
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
				t.Errorf("decode events: %v", err)
			}
			if batch.Epoch != "epoch-1" || len(batch.Events) != 1 || batch.Events[0].Sequence != 8 {
				t.Errorf("event batch = %+v", batch)
			}
			requests <- r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(EventAcknowledgement{Epoch: "epoch-1", AcknowledgedThrough: 8})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	accepted, err := client.Report(context.Background(), NodeReport{
		EventEpoch:         "epoch-1",
		Sequence:           7,
		ObservedGeneration: 3,
		Conditions:         []Condition{},
	}, idempotencyKey)
	if err != nil || !accepted {
		t.Fatalf("report accepted = %v, error = %v", accepted, err)
	}
	acknowledgement, err := client.AppendEvents(context.Background(), EventBatch{
		Epoch: "epoch-1",
		Events: []NodeEvent{{
			ID:         "event-8",
			Epoch:      "epoch-1",
			Sequence:   8,
			Type:       "player.started",
			OccurredAt: time.Unix(100, 0).UTC(),
		}},
	}, idempotencyKey)
	if err != nil {
		t.Fatalf("append events: %v", err)
	}
	if acknowledgement.Epoch != "epoch-1" || acknowledgement.AcknowledgedThrough != 8 {
		t.Fatalf("acknowledgement = %+v", acknowledgement)
	}
	if len(requests) != 2 {
		t.Fatalf("server received %d report/event requests", len(requests))
	}
}

func TestEnrollRetryRequiresAndReusesIdempotency(t *testing.T) {
	const idempotencyKey = "enrollment-key-1234"
	var attempts atomic.Int32
	firstBodies := make(chan string, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read enrollment body: %v", err)
		}
		if got := r.Header.Get("Idempotency-Key"); got != idempotencyKey {
			t.Errorf("Idempotency-Key = %q", got)
		}
		if attempt == 1 {
			firstBodies <- string(body)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		firstBody := <-firstBodies
		if got := string(body); got != firstBody {
			t.Errorf("retried body = %q, first body = %q", got, firstBody)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"certificate":"certificate-pem","expiresAt":"2030-01-01T00:00:00Z","nodeId":"node-1","eventEpoch":"epoch-1","publisherKeyId":"publisher-1","publisherPublicKey":"public-key"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	request := EnrollmentRequest{Token: "one-time-token", PublicKey: "public-key"}
	enrollment, err := client.Enroll(context.Background(), request, idempotencyKey)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if enrollment.NodeID != "node-1" || enrollment.Certificate != "certificate-pem" {
		t.Fatalf("enrollment = %+v", enrollment)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("enrollment attempts = %d, want 2", got)
	}

	if _, err := client.Recover(context.Background(), request, ""); err == nil {
		t.Fatal("recover without durable idempotency key succeeded")
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("missing-key recovery reached server; attempts = %d", got)
	}
}

func TestMutationsRetryOnlyWithIdempotencyKey(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := attempts.Add(1)
		if attempt == 1 || attempt == 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client := newTestClient(t, server, nil)

	_, err := client.Report(context.Background(), NodeReport{}, "")
	var statusError *StatusError
	if !errors.As(err, &statusError) || statusError.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("non-idempotent report error = %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("non-idempotent attempts = %d, want 1", got)
	}

	accepted, err := client.Report(context.Background(), NodeReport{}, "0123456789abcdef")
	if err != nil || !accepted {
		t.Fatalf("idempotent report accepted = %v, error = %v", accepted, err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("total attempts = %d, want 3", got)
	}
}

func newTestClient(t *testing.T, server *httptest.Server, nodeCertificate *tls.Certificate) *Client {
	t.Helper()
	certificate := server.Certificate()
	serverCAPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	client, err := New(Config{
		BaseURL:         server.URL,
		ServerCAPEM:     serverCAPEM,
		NodeCertificate: nodeCertificate,
		RequestTimeout:  2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func makeCertificate(t *testing.T, certificateAuthority bool) (tls.Certificate, []byte) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate private key: %v", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: "test-node"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if certificateAuthority {
		template.IsCA = true
		template.BasicConstraintsValid = true
		template.KeyUsage |= x509.KeyUsageCertSign
		template.ExtKeyUsage = nil
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return certificate, certificatePEM
}
