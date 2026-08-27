package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/release"
)

const (
	defaultRequestTimeout = 15 * time.Second
	defaultRetryBackoff   = 100 * time.Millisecond
	defaultMaxAttempts    = 3
	maxJSONBody           = 4 << 20
)

type Config struct {
	BaseURL         string
	ServerCAPEM     []byte
	NodeCertificate *tls.Certificate
	RequestTimeout  time.Duration
	MaxAttempts     int
	RetryBackoff    time.Duration
}

type Client struct {
	baseURL        string
	httpClient     *http.Client
	requestTimeout time.Duration
	maxAttempts    int
	retryBackoff   time.Duration
}

func New(config Config) (*Client, error) {
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" {
		return nil, errors.New("control-plane base URL must be an absolute HTTPS URL")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("control-plane base URL must not contain user information, a query, or a fragment")
	}
	if len(config.ServerCAPEM) == 0 {
		return nil, errors.New("a control-plane server trust anchor is required")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(config.ServerCAPEM) {
		return nil, errors.New("control-plane server trust anchor is not valid PEM")
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	if config.NodeCertificate != nil {
		tlsConfig.Certificates = []tls.Certificate{*config.NodeCertificate}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig

	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	if requestTimeout < 0 {
		return nil, errors.New("request timeout must not be negative")
	}
	maxAttempts := config.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultMaxAttempts
	}
	if maxAttempts < 1 {
		return nil, errors.New("maximum attempts must be positive")
	}
	retryBackoff := config.RetryBackoff
	if retryBackoff == 0 {
		retryBackoff = defaultRetryBackoff
	}
	if retryBackoff < 0 {
		return nil, errors.New("retry backoff must not be negative")
	}

	return &Client{
		baseURL: strings.TrimRight(base.String(), "/"),
		httpClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		requestTimeout: requestTimeout,
		maxAttempts:    maxAttempts,
		retryBackoff:   retryBackoff,
	}, nil
}

func (c *Client) CloseIdleConnections() {
	c.httpClient.CloseIdleConnections()
}

// Enroll requires a durable idempotency key. The caller must persist and reuse
// that key with the identical token and public key until it receives a result.
func (c *Client) Enroll(ctx context.Context, request EnrollmentRequest, idempotencyKey string) (Enrollment, error) {
	if err := requireIdempotencyKey(idempotencyKey); err != nil {
		return Enrollment{}, err
	}
	var enrollment Enrollment
	err := c.jsonOperation(ctx, http.MethodPost, "/player/enroll", nil, request, idempotencyKey, []int{http.StatusCreated}, &enrollment, 0)
	return enrollment, err
}

// Recover has the same durable-key requirement as Enroll so a response lost
// after token consumption can be replayed atomically by the control plane.
func (c *Client) Recover(ctx context.Context, request EnrollmentRequest, idempotencyKey string) (Enrollment, error) {
	if err := requireIdempotencyKey(idempotencyKey); err != nil {
		return Enrollment{}, err
	}
	var enrollment Enrollment
	err := c.jsonOperation(ctx, http.MethodPost, "/player/recover", nil, request, idempotencyKey, []int{http.StatusCreated}, &enrollment, 0)
	return enrollment, err
}

func (c *Client) RotateCredential(ctx context.Context, idempotencyKey string) (ClientCredential, error) {
	var credential ClientCredential
	err := c.jsonOperation(ctx, http.MethodPost, "/player/credentials:rotate", nil, nil, idempotencyKey, []int{http.StatusCreated}, &credential, 0)
	return credential, err
}

func (c *Client) PutCapabilities(ctx context.Context, report CapabilityReport, idempotencyKey string) error {
	return c.jsonOperation(ctx, http.MethodPut, "/player/capabilities", nil, report, idempotencyKey, []int{http.StatusNoContent}, nil, 0)
}

func (c *Client) GetAssignment(ctx context.Context, etag string, wait time.Duration) (AssignmentResult, error) {
	if wait < 0 || wait > 30*time.Second || wait%time.Second != 0 {
		return AssignmentResult{}, errors.New("assignment wait must be a whole number of seconds between 0 and 30")
	}
	query := make(url.Values)
	query.Set("waitSeconds", strconv.Itoa(int(wait/time.Second)))
	headers := make(http.Header)
	if etag != "" {
		headers.Set("If-None-Match", etag)
	}
	response, cancel, err := c.do(ctx, http.MethodGet, "/player/assignment", query, nil, headers, "", wait)
	if err != nil {
		return AssignmentResult{}, err
	}
	defer cancel()
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		responseETag := response.Header.Get("ETag")
		if responseETag == "" {
			responseETag = etag
		}
		return AssignmentResult{ETag: responseETag, NotModified: true}, nil
	}
	if response.StatusCode != http.StatusOK {
		return AssignmentResult{}, responseError(response, http.MethodGet, "/player/assignment")
	}
	responseETag := response.Header.Get("ETag")
	if responseETag == "" {
		return AssignmentResult{}, &ProtocolError{Method: http.MethodGet, Path: "/player/assignment", Reason: "missing ETag"}
	}
	var assignment Assignment
	if err := decodeJSON(response.Body, &assignment); err != nil {
		return AssignmentResult{}, &ProtocolError{Method: http.MethodGet, Path: "/player/assignment", Reason: "invalid JSON body"}
	}
	if assignment.PresentationKind != release.PresentationMedia && assignment.PresentationKind != release.PresentationCable {
		return AssignmentResult{}, &ProtocolError{Method: http.MethodGet, Path: "/player/assignment", Reason: "invalid presentation kind"}
	}
	return AssignmentResult{Assignment: assignment, ETag: responseETag}, nil
}

func (c *Client) FetchRelease(ctx context.Context, releaseID string) (ReleaseEnvelope, error) {
	if releaseID == "" {
		return ReleaseEnvelope{}, errors.New("release ID is required")
	}
	path := "/player/releases/" + url.PathEscape(releaseID)
	var envelope ReleaseEnvelope
	err := c.jsonOperation(ctx, http.MethodGet, path, nil, nil, "", []int{http.StatusOK}, &envelope, 0)
	return envelope, err
}

// Report returns true for a newly accepted report and false when the server has
// already accepted this sequence or a newer one.
func (c *Client) Report(ctx context.Context, report NodeReport, idempotencyKey string) (bool, error) {
	status, err := c.statusOperation(ctx, http.MethodPost, "/player/reports", report, idempotencyKey, []int{http.StatusAccepted, http.StatusNoContent})
	return status == http.StatusAccepted, err
}

func (c *Client) AppendEvents(ctx context.Context, batch EventBatch, idempotencyKey string) (EventAcknowledgement, error) {
	var acknowledgement EventAcknowledgement
	err := c.jsonOperation(ctx, http.MethodPost, "/player/events:append", nil, batch, idempotencyKey, []int{http.StatusOK}, &acknowledgement, 0)
	return acknowledgement, err
}

func (c *Client) HeadArtifact(ctx context.Context, digest string) (ArtifactMetadata, error) {
	path, err := artifactPath(digest)
	if err != nil {
		return ArtifactMetadata{}, err
	}
	response, cancel, err := c.do(ctx, http.MethodHead, path, nil, nil, nil, "", 0)
	if err != nil {
		return ArtifactMetadata{}, err
	}
	defer cancel()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ArtifactMetadata{}, responseError(response, http.MethodHead, path)
	}
	if response.ContentLength < 0 {
		return ArtifactMetadata{}, &ProtocolError{Method: http.MethodHead, Path: path, Reason: "missing Content-Length"}
	}
	etag := response.Header.Get("ETag")
	if etag == "" {
		return ArtifactMetadata{}, &ProtocolError{Method: http.MethodHead, Path: path, Reason: "missing ETag"}
	}
	return ArtifactMetadata{
		Size:         response.ContentLength,
		ETag:         etag,
		AcceptRanges: response.Header.Get("Accept-Ranges"),
	}, nil
}

func (c *Client) GetArtifact(ctx context.Context, digest string, byteRange *ByteRange, ifRange string) (ArtifactContent, error) {
	path, err := artifactPath(digest)
	if err != nil {
		return ArtifactContent{}, err
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/octet-stream, application/problem+json")
	headers.Set("Accept-Encoding", "identity")
	if byteRange != nil {
		rangeValue, err := byteRange.headerValue()
		if err != nil {
			return ArtifactContent{}, err
		}
		headers.Set("Range", rangeValue)
	}
	if ifRange != "" {
		headers.Set("If-Range", ifRange)
	}
	response, cancel, err := c.do(ctx, http.MethodGet, path, nil, nil, headers, "", 0)
	if err != nil {
		return ArtifactContent{}, err
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		defer cancel()
		defer response.Body.Close()
		return ArtifactContent{}, responseError(response, http.MethodGet, path)
	}
	etag := response.Header.Get("ETag")
	if etag == "" {
		cancel()
		response.Body.Close()
		return ArtifactContent{}, &ProtocolError{Method: http.MethodGet, Path: path, Reason: "missing ETag"}
	}
	return ArtifactContent{
		Body:         &cancelReadCloser{ReadCloser: response.Body, cancel: cancel},
		Size:         response.ContentLength,
		ETag:         etag,
		AcceptRanges: response.Header.Get("Accept-Ranges"),
		ContentRange: response.Header.Get("Content-Range"),
		Partial:      response.StatusCode == http.StatusPartialContent,
	}, nil
}

func (r ByteRange) headerValue() (string, error) {
	if r.Start < 0 {
		return "", errors.New("artifact range start must not be negative")
	}
	if r.End == nil {
		return fmt.Sprintf("bytes=%d-", r.Start), nil
	}
	if *r.End < r.Start {
		return "", errors.New("artifact range end must not precede its start")
	}
	return fmt.Sprintf("bytes=%d-%d", r.Start, *r.End), nil
}

func artifactPath(digest string) (string, error) {
	if len(digest) != 64 || strings.ToLower(digest) != digest {
		return "", errors.New("artifact digest must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return "", errors.New("artifact digest must be 64 lowercase hexadecimal characters")
	}
	return "/player/artifacts/" + digest + "/content", nil
}

func (c *Client) statusOperation(ctx context.Context, method, path string, request any, idempotencyKey string, success []int) (int, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return 0, &RequestError{Method: method, Path: path, Err: err}
	}
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return 0, err
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	response, cancel, err := c.do(ctx, method, path, nil, body, headers, idempotencyKey, 0)
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer response.Body.Close()
	if !containsStatus(success, response.StatusCode) {
		return 0, responseError(response, method, path)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxJSONBody))
	return response.StatusCode, nil
}

func (c *Client) jsonOperation(ctx context.Context, method, path string, query url.Values, request any, idempotencyKey string, success []int, output any, extraTimeout time.Duration) error {
	if err := validateIdempotencyKey(idempotencyKey); err != nil {
		return err
	}
	var body []byte
	var err error
	if request != nil {
		body, err = json.Marshal(request)
		if err != nil {
			return &RequestError{Method: method, Path: path, Err: err}
		}
	}
	headers := make(http.Header)
	if request != nil {
		headers.Set("Content-Type", "application/json")
	}
	response, cancel, err := c.do(ctx, method, path, query, body, headers, idempotencyKey, extraTimeout)
	if err != nil {
		return err
	}
	defer cancel()
	defer response.Body.Close()
	if !containsStatus(success, response.StatusCode) {
		return responseError(response, method, path)
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxJSONBody))
		return nil
	}
	if err := decodeJSON(response.Body, output); err != nil {
		return &ProtocolError{Method: method, Path: path, Reason: "invalid JSON body"}
	}
	return nil
}

func (c *Client) do(parent context.Context, method, path string, query url.Values, body []byte, headers http.Header, idempotencyKey string, extraTimeout time.Duration) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(parent, c.requestTimeout+extraTimeout)
	attempts := 1
	if idempotencyKey != "" {
		attempts = c.maxAttempts
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		requestURL := c.baseURL + path
		if len(query) != 0 {
			requestURL += "?" + query.Encode()
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
		if err != nil {
			cancel()
			return nil, func() {}, &RequestError{Method: method, Path: path, Err: err}
		}
		request.Header = headers.Clone()
		if request.Header == nil {
			request.Header = make(http.Header)
		}
		if request.Header.Get("Accept") == "" {
			request.Header.Set("Accept", "application/json, application/problem+json")
		}
		if idempotencyKey != "" {
			request.Header.Set("Idempotency-Key", idempotencyKey)
		}

		response, err := c.httpClient.Do(request)
		if err != nil {
			if attempt < attempts && retryableTransportError(ctx, err) {
				if !sleepContext(ctx, c.retryBackoff) {
					cancel()
					return nil, func() {}, &RequestError{Method: method, Path: path, Err: ctx.Err()}
				}
				continue
			}
			cancel()
			return nil, func() {}, &RequestError{Method: method, Path: path, Err: err}
		}
		if attempt < attempts && retryableStatus(response.StatusCode) {
			delay := retryDelay(response, c.retryBackoff)
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxJSONBody))
			response.Body.Close()
			if !sleepContext(ctx, delay) {
				cancel()
				return nil, func() {}, &RequestError{Method: method, Path: path, Err: ctx.Err()}
			}
			continue
		}
		return response, cancel, nil
	}
	cancel()
	return nil, func() {}, &RequestError{Method: method, Path: path, Err: errors.New("attempts exhausted")}
}

func requireIdempotencyKey(key string) error {
	if key == "" {
		return errors.New("idempotency key is required")
	}
	return validateIdempotencyKey(key)
}

func validateIdempotencyKey(key string) error {
	if key != "" && (len(key) < 16 || len(key) > 200) {
		return errors.New("idempotency key must contain between 16 and 200 bytes")
	}
	return nil
}

func containsStatus(statuses []int, status int) bool {
	for _, candidate := range statuses {
		if candidate == status {
			return true
		}
	}
	return false
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableTransportError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func retryDelay(response *http.Response, fallback time.Duration) time.Duration {
	value := response.Header.Get("Retry-After")
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if delay := time.Until(at); delay > 0 {
			return delay
		}
		return 0
	}
	return fallback
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func responseError(response *http.Response, method, path string) error {
	status := StatusError{
		Method:       method,
		Path:         path,
		StatusCode:   response.StatusCode,
		ETag:         response.Header.Get("ETag"),
		ContentRange: response.Header.Get("Content-Range"),
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType == "application/problem+json" {
		var problem Problem
		if err := decodeJSON(response.Body, &problem); err == nil && problem.Type != "" && problem.Title != "" && problem.Status != 0 {
			return &ProblemError{StatusError: status, Problem: problem}
		}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxJSONBody))
	return &status
}

func decodeJSON(reader io.Reader, output any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxJSONBody))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("response contains more than one JSON value")
	}
	return nil
}

type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.cancel()
	return err
}
