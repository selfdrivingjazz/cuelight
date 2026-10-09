package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The desktop renderer server also arbitrates a display shared by several
// Cuelight networks and installs desktop software updates; these calls make
// up the rest of the sidecar ↔ Electron local contract.

// OutputStandby reports whether the local display currently shows another
// network (GET /v1/output).
func (l *HTTPBrowserLauncher) OutputStandby(ctx context.Context) (bool, error) {
	response, err := l.request(ctx, http.MethodGet, "/v1/output", nil)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("desktop renderer output returned HTTP %d", response.StatusCode)
	}
	var value struct {
		Standby *bool `json:"standby"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&value); err != nil || value.Standby == nil {
		return false, errors.New("desktop renderer returned an invalid output state")
	}
	return *value.Standby, nil
}

// AssignmentChange tells the arbiter that this membership accepted a different
// assignment; the most recent change wins a shared display.
type AssignmentChange struct {
	Generation    string    `json:"generation"`
	ReleaseDigest *string   `json:"releaseDigest"`
	ChangedAt     time.Time `json:"changedAt"`
}

// NotifyAssignment sends POST /v1/assignment.
func (l *HTTPBrowserLauncher) NotifyAssignment(ctx context.Context, change AssignmentChange) error {
	change.ChangedAt = change.ChangedAt.UTC().Truncate(time.Second)
	return l.expect(ctx, "/v1/assignment", change, "assignment")
}

// StageSoftwareUpdate hands a verified electron-updater feed directory to the
// desktop app (POST /v1/software-update).
func (l *HTTPBrowserLauncher) StageSoftwareUpdate(ctx context.Context, version, feedDir string) error {
	return l.expect(ctx, "/v1/software-update", map[string]string{"version": version, "feedDir": feedDir}, "software update")
}

func (l *HTTPBrowserLauncher) expect(ctx context.Context, endpoint string, body any, name string) error {
	response, err := l.request(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("desktop renderer %s returned HTTP %d", name, response.StatusCode)
	}
	return nil
}
