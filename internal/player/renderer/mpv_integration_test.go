package renderer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMPVRealIPC(t *testing.T) {
	socket := os.Getenv("CUELIGHT_MPV_SOCKET")
	fixtureDirectory := os.Getenv("CUELIGHT_MPV_FIXTURES")
	if socket == "" || fixtureDirectory == "" {
		t.Skip("set CUELIGHT_MPV_SOCKET and CUELIGHT_MPV_FIXTURES for a real mpv smoke")
	}
	adapter := &MPV{SocketPath: socket, Timeout: 5 * time.Second}
	for _, name := range []string{"phase0.jpg", "phase0.png", "phase0.mp4"} {
		t.Run(name, func(t *testing.T) {
			source := filepath.Join(fixtureDirectory, name)
			candidate := Candidate{ReleaseDigest: strings.Repeat("a", 64), Source: source, Fit: "fit"}
			if err := adapter.Start(context.Background(), candidate); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				status, err := adapter.Health(context.Background(), candidate)
				if err == nil && status.Healthy {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("mpv did not report fixture path: status=%#v err=%v", status, err)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if err := adapter.Commit(context.Background(), candidate); err != nil {
				t.Fatal(err)
			}
		})
	}
	if err := adapter.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
