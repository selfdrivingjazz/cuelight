package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const maxErrorBody = 1 << 20

// Client calls the authenticated public operator API.
type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

type options struct {
	allowInsecureLoopback bool
}

// Option configures a Client.
type Option func(*options)

// AllowInsecureLoopback permits plain HTTP only when the server host is a
// loopback IP address or localhost. It is intended for local development.
func AllowInsecureLoopback() Option {
	return func(options *options) {
		options.allowInsecureLoopback = true
	}
}

func New(baseURL, token string, httpClient *http.Client, provided ...Option) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("server URL must be an absolute http or https URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("server URL must not contain a query or fragment")
	}
	config := options{}
	for _, option := range provided {
		if option != nil {
			option(&config)
		}
	}
	if !secureServerURL(parsed, config.allowInsecureLoopback) {
		return nil, errors.New("operator bearer tokens require HTTPS; plain HTTP may be explicitly enabled only for loopback development")
	}
	if token == "" {
		return nil, errors.New("operator token is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	configuredHTTPClient := *httpClient
	previousRedirectPolicy := configuredHTTPClient.CheckRedirect
	configuredHTTPClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !secureServerURL(request.URL, config.allowInsecureLoopback) {
			return errors.New("refusing to redirect an operator bearer token to insecure HTTP")
		}
		if previousRedirectPolicy != nil {
			return previousRedirectPolicy(request, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	parsed.RawPath = ""
	return &Client{baseURL: parsed, token: token, httpClient: &configuredHTTPClient}, nil
}

func secureServerURL(server *url.URL, allowInsecureLoopback bool) bool {
	return server.Scheme == "https" || (server.Scheme == "http" && allowInsecureLoopback && isLoopbackHost(server.Hostname()))
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (c *Client) ListTokens(ctx context.Context) ([]AdminToken, error) {
	var response struct {
		Items []AdminToken `json:"items"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/admin-tokens", nil, "", "", &response, http.StatusOK)
	if err == nil && response.Items == nil {
		response.Items = []AdminToken{}
	}
	return response.Items, err
}

func (c *Client) RevokeToken(ctx context.Context, tokenID, idempotencyKey string) error {
	return c.doJSON(ctx, http.MethodPost, "/admin-tokens/"+url.PathEscape(tokenID)+"/revoke", nil, idempotencyKey, "", nil, http.StatusNoContent)
}

func (c *Client) CreateEnrollmentToken(ctx context.Context, idempotencyKey string) (OneTimeToken, error) {
	var result OneTimeToken
	err := c.doJSON(ctx, http.MethodPost, "/enrollment-tokens", nil, idempotencyKey, "", &result, http.StatusCreated)
	return result, err
}

func (c *Client) CreateRecoveryToken(ctx context.Context, nodeID, idempotencyKey string) (OneTimeToken, error) {
	var result OneTimeToken
	path := "/nodes/" + url.PathEscape(nodeID) + "/recovery-tokens"
	err := c.doJSON(ctx, http.MethodPost, path, nil, idempotencyKey, "", &result, http.StatusCreated)
	return result, err
}

func (c *Client) ListNodes(ctx context.Context) ([]Node, error) {
	var response struct {
		Items []Node `json:"items"`
	}
	err := c.doJSON(ctx, http.MethodGet, "/nodes", nil, "", "", &response, http.StatusOK)
	if err == nil && response.Items == nil {
		response.Items = []Node{}
	}
	return response.Items, err
}

// ListNodesPage fetches one server-paginated node page. The older ListNodes
// method remains a compatibility convenience and reads only the first page.
func (c *Client) ListNodesPage(ctx context.Context, request PageRequest) (Page[Node], error) {
	path, err := pagePath("/nodes", request)
	if err != nil {
		return Page[Node]{}, err
	}
	var response Page[Node]
	err = c.doJSON(ctx, http.MethodGet, path, nil, "", "", &response, http.StatusOK)
	if err == nil && response.Items == nil {
		response.Items = []Node{}
	}
	return response, err
}

func (c *Client) OperatorMe(ctx context.Context) (OperatorMe, error) {
	var result OperatorMe
	err := c.doJSON(ctx, http.MethodGet, "/operator/me", nil, "", "", &result, http.StatusOK)
	return result, err
}
func (c *Client) GetNode(ctx context.Context, nodeID string) (Node, error) {

	var result Node
	err := c.doJSON(ctx, http.MethodGet, "/nodes/"+url.PathEscape(nodeID), nil, "", "", &result, http.StatusOK)
	return result, err
}

func (c *Client) ListAssetsPage(ctx context.Context, request PageRequest) (Page[Asset], error) {
	path, err := pagePath("/assets", request)
	if err != nil {
		return Page[Asset]{}, err
	}
	var response Page[Asset]
	err = c.doJSON(ctx, http.MethodGet, path, nil, "", "", &response, http.StatusOK)
	if err == nil && response.Items == nil {
		response.Items = []Asset{}
	}
	return response, err
}

func (c *Client) ListUploadsPage(ctx context.Context, request PageRequest) (Page[Upload], error) {
	path, err := pagePath("/uploads", request)
	if err != nil {
		return Page[Upload]{}, err
	}
	var response Page[Upload]
	err = c.doJSON(ctx, http.MethodGet, path, nil, "", "", &response, http.StatusOK)
	if err == nil && response.Items == nil {
		response.Items = []Upload{}
	}
	return response, err
}

func (c *Client) ListReleasesPage(ctx context.Context, request PageRequest) (Page[Document], error) {
	path, err := pagePath("/releases", request)
	if err != nil {
		return Page[Document]{}, err
	}
	var response Page[Document]
	err = c.doJSON(ctx, http.MethodGet, path, nil, "", "", &response, http.StatusOK)
	if err == nil && response.Items == nil {
		response.Items = []Document{}
	}
	return response, err
}

func (c *Client) ListAssignmentsPage(ctx context.Context, request PageRequest) (Page[Assignment], error) {
	path, err := pagePath("/assignments", request)
	if err != nil {
		return Page[Assignment]{}, err
	}
	var response Page[Assignment]
	err = c.doJSON(ctx, http.MethodGet, path, nil, "", "", &response, http.StatusOK)
	if err == nil && response.Items == nil {
		response.Items = []Assignment{}
	}
	return response, err
}
func (c *Client) GetAssignment(ctx context.Context, nodeID string) (Assignment, error) {
	var result Assignment
	path := "/nodes/" + url.PathEscape(nodeID) + "/assignment"
	err := c.doJSON(ctx, http.MethodGet, path, nil, "", "", &result, http.StatusOK)
	return result, err
}

func (c *Client) GetStatus(ctx context.Context, nodeID string) (Document, error) {
	var result Document
	path := "/nodes/" + url.PathEscape(nodeID) + "/status"
	err := c.doJSON(ctx, http.MethodGet, path, nil, "", "", &result, http.StatusOK)
	return result, err
}

func (c *Client) ListEvents(ctx context.Context, nodeID, cursor string) (EventPage, error) {
	path := "/nodes/" + url.PathEscape(nodeID) + "/events"
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	var result EventPage
	err := c.doJSON(ctx, http.MethodGet, path, nil, "", "", &result, http.StatusOK)
	if err == nil && result.Items == nil {
		result.Items = []Document{}
	}
	return result, err
}

func (c *Client) CreateUpload(ctx context.Context, name string, length int64, mediaType, idempotencyKey string) (Upload, error) {
	body := struct {
		Name      string `json:"name"`
		Length    int64  `json:"length"`
		MediaType string `json:"mediaType"`
	}{Name: name, Length: length, MediaType: mediaType}
	var result Upload
	err := c.doJSON(ctx, http.MethodPost, "/uploads", body, idempotencyKey, "", &result, http.StatusCreated)
	return result, err
}

func (c *Client) AppendUpload(ctx context.Context, uploadID string, offset, length int64, content io.Reader) (int64, error) {
	path := "/uploads/" + url.PathEscape(uploadID)
	request, err := c.request(ctx, http.MethodPatch, path, content)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/offset+octet-stream")
	request.Header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	request.ContentLength = length
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("send API request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return 0, decodeProblem(response)
	}
	next, err := strconv.ParseInt(response.Header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		return 0, errors.New("API response has invalid Upload-Offset header")
	}
	return next, nil
}

func (c *Client) CompleteUpload(ctx context.Context, uploadID, idempotencyKey string) (Asset, error) {
	var result Asset
	path := "/uploads/" + url.PathEscape(uploadID) + "/complete"
	err := c.doJSON(ctx, http.MethodPost, path, nil, idempotencyKey, "", &result, http.StatusCreated)
	return result, err
}

func (c *Client) UploadAsset(ctx context.Context, name, mediaType string, length int64, content io.Reader, idempotencyKey string) (Asset, error) {
	upload, err := c.CreateUpload(ctx, name, length, mediaType, idempotencyKey)
	if err != nil {
		return Asset{}, err
	}
	next, err := c.AppendUpload(ctx, upload.ID, 0, length, content)
	if err != nil {
		return Asset{}, err
	}
	if next != length {
		return Asset{}, fmt.Errorf("upload accepted %d of %d bytes", next, length)
	}
	return c.CompleteUpload(ctx, upload.ID, idempotencyKey)
}

func (c *Client) RegisterLocalAsset(ctx context.Context, path, idempotencyKey string) (Asset, error) {
	var result Asset
	body := struct {
		Path string `json:"path"`
	}{Path: path}
	err := c.doJSON(ctx, http.MethodPost, "/assets/register-local", body, idempotencyKey, "", &result, http.StatusCreated)
	return result, err
}

func (c *Client) PublishRelease(ctx context.Context, input json.RawMessage, idempotencyKey string) (Document, error) {
	if !json.Valid(input) {
		return nil, errors.New("release input is not valid JSON")
	}
	var result Document
	err := c.doJSON(ctx, http.MethodPost, "/releases/publish", input, idempotencyKey, "", &result, http.StatusCreated)
	return result, err
}

func (c *Client) PutAssignment(ctx context.Context, nodeID, ifMatch, idempotencyKey string, input AssignmentInput) (Assignment, error) {
	var result Assignment
	path := "/nodes/" + url.PathEscape(nodeID) + "/assignment"
	err := c.doJSON(ctx, http.MethodPut, path, input, idempotencyKey, ifMatch, &result, http.StatusOK, http.StatusCreated)
	return result, err
}

func pagePath(path string, request PageRequest) (string, error) {
	if request.Limit < 0 || request.Limit > 100 {
		return "", errors.New("page limit must be between 1 and 100")
	}
	values := url.Values{}
	if request.Cursor != "" {
		values.Set("cursor", request.Cursor)
	}
	if request.Limit != 0 {
		values.Set("limit", strconv.Itoa(request.Limit))
	}
	if request.Sort != "" {
		values.Set("sort", request.Sort)
	}
	if request.Order != "" {
		values.Set("order", request.Order)
	}
	if request.Search != "" {
		values.Set("search", request.Search)
	}
	for key, value := range request.Filters {
		if key != "" && value != "" {
			values.Set(key, value)
		}
	}
	if encoded := values.Encode(); encoded != "" {
		return path + "?" + encoded, nil
	}
	return path, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, input any, idempotencyKey, ifMatch string, output any, expected ...int) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode API request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("send API request: %w", err)
	}
	defer response.Body.Close()
	for _, status := range expected {
		if response.StatusCode == status {
			if output == nil || response.StatusCode == http.StatusNoContent {
				_, err = io.Copy(io.Discard, response.Body)
				return err
			}
			if err := json.NewDecoder(response.Body).Decode(output); err != nil {
				return fmt.Errorf("decode API response: %w", err)
			}
			return nil
		}
	}
	return decodeProblem(response)
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	reference, err := url.Parse(strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, fmt.Errorf("build API URL: %w", err)
	}
	endpoint := c.baseURL.ResolveReference(reference)
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build API request: %w", err)
	}
	request.Header.Set("Accept", "application/json, application/problem+json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	return request, nil
}

func decodeProblem(response *http.Response) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	if err != nil {
		return fmt.Errorf("read API error response: %w", err)
	}
	problem := &Problem{
		Type:   "about:blank",
		Title:  http.StatusText(response.StatusCode),
		Status: response.StatusCode,
	}
	if len(data) != 0 {
		if err := json.Unmarshal(data, problem); err != nil {
			problem.Detail = strings.TrimSpace(string(data))
		}
	}
	if problem.Type == "" {
		problem.Type = "about:blank"
	}
	if problem.Title == "" {
		problem.Title = http.StatusText(response.StatusCode)
	}
	if problem.Status == 0 {
		problem.Status = response.StatusCode
	}
	return problem
}
