package renderer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyPreservesRangeAndHidesUpstream(t *testing.T) {
	const digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/player/artifacts/"+digest+"/content" {
			t.Fatalf("unexpected upstream path %q", request.URL.Path)
		}
		if request.Header.Get("Range") != "bytes=2-4" || request.Header.Get("If-Range") != `"etag"` {
			t.Fatalf("range headers not forwarded: %#v", request.Header)
		}
		writer.Header().Set("Accept-Ranges", "bytes")
		writer.Header().Set("Content-Range", "bytes 2-4/6")
		writer.Header().Set("ETag", `"etag"`)
		writer.WriteHeader(http.StatusPartialContent)
		writer.Write([]byte("ble"))
	}))
	defer upstream.Close()

	proxy, err := NewProxy(upstream.URL+"/api/v1", upstream.Client())
	if err != nil {
		t.Fatal(err)
	}
	path, err := proxy.Grant(digest, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(proxy)
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=2-4")
	request.Header.Set("If-Range", `"etag"`)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || string(body) != "ble" {
		t.Fatalf("unexpected proxy response %d %q", response.StatusCode, body)
	}
	if strings.Contains(path, digest[:16]) && len(strings.Split(path, "/")[2]) < 32 {
		t.Fatal("proxy capability is not random")
	}

	proxy.Revoke(path)
	response, err = server.Client().Get(server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked capability returned %d", response.StatusCode)
	}
}

func TestProxyRenewsAndExpiresCapability(t *testing.T) {
	proxy, err := NewProxy("https://control.invalid/api/v1", http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	proxy.now = func() time.Time { return now }
	path, err := proxy.Grant(strings.Repeat("a", 64), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(500 * time.Millisecond)
	if err := proxy.Renew(path, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	token := strings.Split(path, "/")[2]
	if got := proxy.grants[token].expires; !got.Equal(now.Add(2 * time.Second)) {
		t.Fatalf("renewed expiry = %v", got)
	}
	now = now.Add(3 * time.Second)
	request := httptest.NewRequest(http.MethodGet, path, nil)
	writer := httptest.NewRecorder()
	proxy.ServeHTTP(writer, request)
	if writer.Code != http.StatusUnauthorized {
		t.Fatalf("expired capability returned %d", writer.Code)
	}
	if err := proxy.Renew(path, time.Second); err == nil {
		t.Fatal("expired capability renewed")
	}
}

func TestMPVAdapterUsesJSONIPC(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "mpv.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	var mu sync.Mutex
	currentPath := ""
	properties := make(map[string]any)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				var request struct {
					Command   []any  `json:"command"`
					RequestID uint64 `json:"request_id"`
				}
				if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&request); err != nil {
					return
				}
				command := fmt.Sprint(request.Command[0])
				response := map[string]any{"error": "success", "request_id": request.RequestID}
				mu.Lock()
				switch command {
				case "loadfile":
					currentPath = fmt.Sprint(request.Command[1])
				case "set_property":
					properties[fmt.Sprint(request.Command[1])] = request.Command[2]
				case "get_property":
					name := fmt.Sprint(request.Command[1])
					if name == "path" {
						response["data"] = currentPath
					} else {
						response["data"] = properties[name]
					}
				case "stop":
					currentPath = ""
				}
				mu.Unlock()
				_ = json.NewEncoder(connection).Encode(response)
			}()
		}
	}()

	adapter := &MPV{SocketPath: socket, Timeout: time.Second}
	candidate := Candidate{ReleaseDigest: strings.Repeat("a", 64), Source: "http://127.0.0.1/media/token/artifact", Fit: "fill", Loop: true, Muted: true, Volume: 42, ImageDurationSecond: 8, Orientation: 90}
	if err := adapter.Start(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	status, err := adapter.Health(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Healthy || status.Path != candidate.Source {
		t.Fatalf("unexpected health: %#v", status)
	}
	mu.Lock()
	if fmt.Sprint(properties["panscan"]) != "1" || properties["loop-file"] != "inf" || properties["mute"] != true || fmt.Sprint(properties["volume"]) != "42" || fmt.Sprint(properties["image-display-duration"]) != "8" || fmt.Sprint(properties["video-rotate"]) != "90" {
		t.Fatalf("signed playback properties not applied: %#v", properties)
	}
	mu.Unlock()
	if err := adapter.Commit(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, err := adapter.Current(context.Background()); err != nil || status.Healthy {
		t.Fatalf("unexpected current state: %#v %v", status, err)
	}
}
