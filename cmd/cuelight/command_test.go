package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	operator "github.com/jmilldotdev/cuelight/internal/client"
)

// newAPIServer serves handler at /api/v1, where a bare --server origin points.
func newAPIServer(handler http.Handler) *httptest.Server {
	return httptest.NewServer(http.StripPrefix("/api/v1", handler))
}

func TestAssignmentCommandMapsRequest(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/nodes/node-1/assignment" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("If-Match"); got != `"generation-4"` {
			t.Errorf("If-Match = %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != "0123456789abcdef" {
			t.Errorf("Idempotency-Key = %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		var input operator.AssignmentInput
		if err := json.Unmarshal(body, &input); err != nil {
			t.Error(err)
		}
		if input.MediaID != "media-1" || input.PlaylistID != "" || input.ExpectedGeneration != 4 || input.DeliveryMode != "cache_then_play" || !input.RemoteInput {
			t.Errorf("input = %#v", input)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		fmt.Fprint(response, `{"mediaId":"media-1","releaseId":"release-1","releaseDigest":"sha256:abc","deliveryMode":"cache_then_play","expectedGeneration":4,"desiredGeneration":5}`)
	}))
	defer server.Close()

	result := executeCLI(t, server, nil,
		"assignment", "set",
		"--media-id", "media-1",
		"--remote-input",
		"--delivery-mode", "cache_then_play",
		"--expected-generation", "4",
		"--if-match", `"generation-4"`,
		"--idempotency-key", "0123456789abcdef",
		"node-1",
	)
	if result.code != exitOK {
		t.Fatalf("code = %d, stderr = %s", result.code, result.stderr)
	}
	if !strings.Contains(result.stdout, "desired generation 5") {
		t.Fatalf("stdout = %q", result.stdout)
	}
}

func TestNodeListJSONAndHumanOutput(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/nodes" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		fmt.Fprint(response, `{"items":[{"id":"node-1","name":"Lobby","credentialState":"active","desiredGeneration":3,"observedGeneration":2}]}`)
	}))
	defer server.Close()

	human := executeCLI(t, server, nil, "node", "list")
	if human.code != exitOK || !strings.Contains(human.stdout, "ID\tNAME") || !strings.Contains(human.stdout, "node-1\tLobby\tactive\t3\t2") {
		t.Fatalf("human result = %#v", human)
	}
	machine := executeCLI(t, server, nil, "--json", "node", "list")
	if machine.code != exitOK {
		t.Fatalf("code = %d, stderr = %s", machine.code, machine.stderr)
	}
	var output struct {
		Items []operator.Node `json:"items"`
	}
	if err := json.Unmarshal([]byte(machine.stdout), &output); err != nil {
		t.Fatalf("JSON output: %v", err)
	}
	if len(output.Items) != 1 || output.Items[0].ID != "node-1" {
		t.Fatalf("output = %#v", output)
	}
}

func TestProblemRenderingHasStableExitCategories(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/problem+json")
		response.WriteHeader(http.StatusNotFound)
		fmt.Fprint(response, `{"type":"https://cuelight.test/problems/not-found","title":"Node not found","status":404,"detail":"no such node","correlationId":"corr-42"}`)
	}))
	defer server.Close()

	human := executeCLI(t, server, nil, "node", "show", "missing")
	if human.code != exitNotFound || human.stdout != "" || !strings.Contains(human.stderr, "Node not found: no such node (HTTP 404) [correlation corr-42]") {
		t.Fatalf("human result = %#v", human)
	}
	machine := executeCLI(t, server, nil, "--json", "node", "show", "missing")
	if machine.code != exitNotFound {
		t.Fatalf("code = %d", machine.code)
	}
	var problem operator.Problem
	if err := json.Unmarshal([]byte(machine.stderr), &problem); err != nil {
		t.Fatalf("problem JSON: %v", err)
	}
	if problem.Status != 404 || problem.CorrelationID != "corr-42" {
		t.Fatalf("problem = %#v", problem)
	}
}

func TestSecretsPrintOnlyFromCreationCommands(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/admin-tokens":
			fmt.Fprint(response, `{"items":[{"id":"token-1","prefix":"cab_123","scopes":["node_read"],"revoked":false,"secret":"must-not-print"}]}`)
		case "/enrollment-tokens":
			response.WriteHeader(http.StatusCreated)
			fmt.Fprint(response, `{"id":"enroll-1","secret":"one-time-secret","expiresAt":"2030-01-01T00:00:00Z"}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	listed := executeCLI(t, server, nil, "--json", "token", "list")
	if listed.code != exitOK || strings.Contains(listed.stdout, "must-not-print") || strings.Contains(listed.stdout, "secret") {
		t.Fatalf("token list leaked secret: %#v", listed)
	}
	configured := executeCLI(t, server, nil, "--json", "config")
	if configured.code != exitOK || strings.Contains(configured.stdout, "operator-secret") {
		t.Fatalf("config leaked secret: %#v", configured)
	}
	created := executeCLI(t, server, nil, "--json", "enrollment-token", "create", "--idempotency-key", "0123456789abcdef")
	if created.code != exitOK || !strings.Contains(created.stdout, "one-time-secret") {
		t.Fatalf("creation omitted secret: %#v", created)
	}
}

func TestLoginWritesPrivateConfigWithoutEchoingToken(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "cuelight", "config.json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--config", configPath,
		"--allow-insecure-loopback",
		"login", "--server", server.URL, "--token", "login-secret",
	}, commandIO{
		stdin:      strings.NewReader(""),
		stdout:     &stdout,
		stderr:     &stderr,
		httpClient: server.Client(),
	})
	if code != exitOK {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "login-secret") || strings.Contains(stderr.String(), "login-secret") {
		t.Fatalf("login echoed token: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %04o", info.Mode().Perm())
	}
	config, err := operator.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if config.Server != server.URL || config.Token != "login-secret" {
		t.Fatalf("config = %#v", config)
	}
}

func TestTokenlessCLISendsNoAuthorization(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if _, present := request.Header["Authorization"]; present {
			t.Errorf("token-less request carried Authorization: %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		fmt.Fprint(response, `{"items":[]}`)
	}))
	defer server.Close()
	configPath := filepath.Join(t.TempDir(), "cuelight", "config.json")
	execute := func(args ...string) cliResult {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"--config", configPath, "--allow-insecure-loopback"}, args...), commandIO{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, httpClient: server.Client()})
		return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
	}
	// --server alone works before any login.
	if result := execute("--server", server.URL, "node", "list"); result.code != exitOK {
		t.Fatalf("--server without config = %#v", result)
	}
	if result := execute("login", "--server", server.URL); result.code != exitOK {
		t.Fatalf("token-less login = %#v", result)
	}
	if result := execute("config"); result.code != exitOK || !strings.Contains(result.stdout, "Token: none") {
		t.Fatalf("config show = %#v", result)
	}
	if result := execute("node", "list"); result.code != exitOK {
		t.Fatalf("node list from token-less config = %#v", result)
	}
}

func TestMediaUploadCommandStreamsFile(t *testing.T) {
	content := []byte("media payload from disk")
	path := filepath.Join(t.TempDir(), "clip.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	step := 0
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		step++
		switch step {
		case 1:
			if request.URL.Path != "/uploads" {
				t.Errorf("create path = %q", request.URL.Path)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			fmt.Fprintf(response, `{"id":"upload-1","length":%d,"offset":0,"state":"open"}`, len(content))
		case 2:
			if request.Method != http.MethodPatch || request.ContentLength != int64(len(content)) {
				t.Errorf("append = %s length %d", request.Method, request.ContentLength)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil || !bytes.Equal(body, content) {
				t.Errorf("upload body = %q, err = %v", body, err)
			}
			response.Header().Set("Upload-Offset", fmt.Sprint(len(content)))
			response.WriteHeader(http.StatusNoContent)
		case 3:
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			fmt.Fprint(response, `{"id":"asset-1","name":"clip.bin","artifact":{"digest":"sha256:abc","size":23,"mediaType":"application/octet-stream"}}`)
		}
	}))
	defer server.Close()

	result := executeCLI(t, server, nil, "--json", "media", "upload", "--idempotency-key", "0123456789abcdef", path)
	if result.code != exitOK || step != 3 || !strings.Contains(result.stdout, `"digest": "sha256:abc"`) {
		t.Fatalf("result = %#v, step = %d", result, step)
	}
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func executeCLI(t *testing.T, server *httptest.Server, stdin io.Reader, args ...string) cliResult {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := operator.SaveConfig(configPath, operator.Config{Server: server.URL, Token: "operator-secret"}); err != nil {
		t.Fatal(err)
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	fullArgs := append([]string{"--config", configPath, "--allow-insecure-loopback"}, args...)
	code := run(context.Background(), fullArgs, commandIO{
		stdin:      stdin,
		stdout:     &stdout,
		stderr:     &stderr,
		httpClient: server.Client(),
	})
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestMediaListFollowsCursorsAndFiltersKind(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.URL.Path != "/media" || query.Get("kind") != "web" {
			t.Errorf("request = %s", request.URL)
		}
		response.Header().Set("Content-Type", "application/json")
		if query.Get("cursor") == "" {
			fmt.Fprint(response, `{"items":[{"id":"m1","kind":"web","title":"Turf","url":"https://example.test/?a"}],"page":{"nextCursor":"c2","previousCursor":null,"hasMore":true}}`)
			return
		}
		if query.Get("cursor") != "c2" {
			t.Errorf("cursor = %q", query.Get("cursor"))
		}
		fmt.Fprint(response, `{"items":[{"id":"m2","kind":"file","title":"Clip","artifact":{"mediaType":"video/mp4"}}],"page":{"nextCursor":null,"previousCursor":"c1","hasMore":false}}`)
	}))
	defer server.Close()

	result := executeCLI(t, server, nil, "media", "list", "--kind", "web")
	if result.code != exitOK || !strings.Contains(result.stdout, "m1\tweb\tTurf\thttps://example.test/?a") || !strings.Contains(result.stdout, "m2\tfile\tClip\tvideo/mp4") {
		t.Fatalf("result = %#v", result)
	}
	limited := executeCLI(t, server, nil, "--output", "jsonl", "media", "list", "--kind", "web", "--limit", "1")
	if limited.code != exitOK || strings.Count(limited.stdout, "\n") != 1 {
		t.Fatalf("limited = %#v", limited)
	}
}

func TestMediaAddWebSendsAppAccess(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var input map[string]any
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if request.Method != http.MethodPost || request.URL.Path != "/media" || input["kind"] != "web" || input["appAccess"] != true {
			t.Errorf("request = %s %s %v", request.Method, request.URL.Path, input)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		fmt.Fprint(response, `{"id":"m1","kind":"web","appAccess":true}`)
	}))
	defer server.Close()

	result := executeCLI(t, server, nil, "media", "add-web", "--url", "https://example.test/", "--app-access")
	if result.code != exitOK {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlaylistCreatePreservesItemOrder(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/playlists" || request.Header.Get("Idempotency-Key") != "key-1" {
			t.Errorf("request = %s %s key %q", request.Method, request.URL.Path, request.Header.Get("Idempotency-Key"))
		}
		var input operator.PlaylistInput
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		want := []operator.PlaylistItemInput{{MediaID: "web-b", Position: 1, DurationSeconds: 30}, {MediaID: "web-a", Position: 2, DurationSeconds: 600}}
		if input.Name != "Turf" || fmt.Sprint(input.Items) != fmt.Sprint(want) {
			t.Errorf("input = %#v", input)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		fmt.Fprint(response, `{"id":"p1","name":"Turf","version":1,"items":[{"mediaId":"web-b","position":1,"durationSeconds":30,"title":"B"}]}`)
	}))
	defer server.Close()

	result := executeCLI(t, server, nil, "playlist", "create", "--name", "Turf", "--item", "web-b:30", "--item", "web-a:600", "--idempotency-key", "key-1")
	if result.code != exitOK || !strings.Contains(result.stdout, "ID: p1") || !strings.Contains(result.stdout, "1\tweb-b\t30\tB") {
		t.Fatalf("result = %#v", result)
	}
	if bad := executeCLI(t, server, nil, "playlist", "create", "--name", "Turf", "--item", "web-b:soon"); bad.code != exitUsage {
		t.Fatalf("bad duration = %#v", bad)
	}
}

func TestPlaylistCreateSurfacesServerValidation(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/problem+json")
		response.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(response, `{"type":"about:blank","title":"Bad Request","status":400,"detail":"invalid argument: item 1 needs a positive durationSeconds"}`)
	}))
	defer server.Close()

	result := executeCLI(t, server, nil, "playlist", "create", "--name", "Turf", "--item", "web-a")
	if result.code != exitAPI || !strings.Contains(result.stderr, "item 1 needs a positive durationSeconds (HTTP 400)") {
		t.Fatalf("result = %#v", result)
	}
}

func TestPlaylistListAndShow(t *testing.T) {
	server := newAPIServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/playlists":
			fmt.Fprint(response, `{"items":[{"id":"p1","name":"Turf","version":2,"items":[{},{}]}],"page":{"nextCursor":null,"previousCursor":null,"hasMore":false}}`)
		case "/playlists/p1":
			fmt.Fprint(response, `{"id":"p1","name":"Turf","version":2,"items":[{"mediaId":"m1","position":1,"durationSeconds":45,"title":"One"}]}`)
		default:
			t.Errorf("path = %s", request.URL.Path)
		}
	}))
	defer server.Close()

	if list := executeCLI(t, server, nil, "playlist", "list"); list.code != exitOK || !strings.Contains(list.stdout, "p1\tTurf\t2\t2") {
		t.Fatalf("list = %#v", list)
	}
	if show := executeCLI(t, server, nil, "playlist", "show", "p1"); show.code != exitOK || !strings.Contains(show.stdout, "1\tm1\t45\tOne") {
		t.Fatalf("show = %#v", show)
	}
}

func TestHelpNeedsNoConfig(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	cases := [][]string{
		{"media", "--help"},
		{"playlist", "-h"},
		{"token", "list", "--help"},
		{"node", "show", "-h"},
		{"media", "list", "--help"},
		{"media", "upload", "--help"},
		{"playlist", "create", "--help"},
		{"config", "show", "--help"},
		{"status", "--help"},
	}
	for groupName := range commandUsage {
		cases = append(cases, []string{groupName, "--help"})
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"--config", missing}, args...), commandIO{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr})
		if code != exitOK || !strings.Contains(stdout.String(), "usage") || stderr.Len() != 0 {
			t.Errorf("%v: code %d stdout %q stderr %q", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout bytes.Buffer
	run(context.Background(), []string{"--config", missing, "playlist", "create", "--help"}, commandIO{stdout: &stdout, stderr: io.Discard})
	if !strings.Contains(stdout.String(), "-item") || !strings.Contains(stdout.String(), "-idempotency-key") {
		t.Fatalf("playlist create help = %q", stdout.String())
	}
}
