package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/buildinfo"
	playerclient "github.com/selfdrivingjazz/cuelight/internal/player/client"
	"github.com/selfdrivingjazz/cuelight/internal/player/renderer"
	playerruntime "github.com/selfdrivingjazz/cuelight/internal/player/runtime"
	"github.com/selfdrivingjazz/cuelight/internal/player/update"
)

const outputPollInterval = 2 * time.Second

// capabilityController adds the desktop output state to every capability
// report and re-sends the last report when that state changes. It embeds the
// concrete client so the runtime still sees its optional interfaces.
type capabilityController struct {
	*playerclient.Client

	mu      sync.Mutex
	last    *playerclient.CapabilityReport
	key     string
	standby bool
}

func (c *capabilityController) PutCapabilities(ctx context.Context, report playerclient.CapabilityReport, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last, c.key = &report, key
	report.OutputStandby = c.standby
	return c.Client.PutCapabilities(ctx, report, key)
}

// setStandby records the output state and re-sends the last report on change.
// A failed re-send leaves the old state recorded so the next poll retries.
func (c *capabilityController) setStandby(ctx context.Context, standby bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.standby == standby {
		return nil
	}
	if c.last == nil {
		c.standby = standby
		return nil
	}
	report := *c.last
	report.OutputStandby = standby
	if err := c.Client.PutCapabilities(ctx, report, c.key); err != nil {
		return err
	}
	c.standby = standby
	return nil
}

// watchOutput polls the desktop renderer's output state.
func watchOutput(ctx context.Context, launcher *renderer.HTTPBrowserLauncher, controller *capabilityController) {
	ticker := time.NewTicker(outputPollInterval)
	defer ticker.Stop()
	for {
		requestCtx, cancel := context.WithTimeout(ctx, outputPollInterval)
		standby, err := launcher.OutputStandby(requestCtx)
		cancel()
		if err == nil {
			if err := controller.setStandby(ctx, standby); err != nil && ctx.Err() == nil {
				log.Printf("report output standby: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// nodeHooks connects runtime controller traffic to the health marker, the
// software update manager, and the desktop display arbiter.
type nodeHooks struct {
	healthPath    string
	healthWritten bool
	updates       *update.Manager
	desktop       *renderer.HTTPBrowserLauncher
	changePath    string
	// announced is the last change the arbiter acknowledged; pending is one it
	// has not acknowledged yet and is retried after each controller poll.
	announced *assignmentChange
	pending   *assignmentChange
	loaded    bool
}

// assignmentChange is the last assignment announced to the desktop arbiter,
// persisted so a restarted sidecar re-announces it with its original time.
type assignmentChange struct {
	Generation    string    `json:"generation"`
	ReleaseDigest string    `json:"releaseDigest,omitempty"`
	ChangedAt     time.Time `json:"changedAt"`
}

func (h *nodeHooks) ControllerReachable(ctx context.Context) {
	if h.pending != nil {
		h.post(ctx, *h.pending)
	}
	if h.healthPath == "" || h.healthWritten {
		return
	}
	if err := update.WriteHealth(h.healthPath, buildinfo.Version, time.Now()); err != nil {
		log.Printf("write health marker: %v", err)
		return
	}
	h.healthWritten = true
}

func (h *nodeHooks) AssignmentReceived(ctx context.Context, assignment playerclient.Assignment) {
	if h.updates != nil {
		h.updates.Offer(assignment.Software)
	}
	if h.desktop != nil {
		h.announce(ctx, assignment)
	}
}

func (h *nodeHooks) DrainEvents() []playerruntime.ExternalEvent {
	if h.updates == nil {
		return nil
	}
	return h.updates.DrainEvents()
}

// announce tells the arbiter when the (generation, release) pair changes; a
// software-only target change is not an assignment change. After a restart
// the persisted change is re-announced with its original time so a restarted
// sidecar does not look like the most recent change on a shared display.
func (h *nodeHooks) announce(ctx context.Context, assignment playerclient.Assignment) {
	restarted := !h.loaded
	if restarted {
		h.loaded = true
		h.announced = readAssignmentChange(h.changePath)
	}
	change := assignmentChange{Generation: strconv.FormatInt(assignment.DesiredGeneration, 10), ReleaseDigest: assignment.ReleaseDigest, ChangedAt: time.Now().UTC().Truncate(time.Second)}
	if previous := h.announced; previous != nil && previous.Generation == change.Generation && previous.ReleaseDigest == change.ReleaseDigest {
		if !restarted {
			h.pending = nil
			return
		}
		change.ChangedAt = previous.ChangedAt
	}
	h.post(ctx, change)
}

func (h *nodeHooks) post(ctx context.Context, change assignmentChange) {
	var digest *string
	if change.ReleaseDigest != "" {
		digest = &change.ReleaseDigest
	}
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := h.desktop.NotifyAssignment(requestCtx, renderer.AssignmentChange{Generation: change.Generation, ReleaseDigest: digest, ChangedAt: change.ChangedAt}); err != nil {
		log.Printf("announce assignment to desktop app: %v", err)
		h.pending = &change
		return
	}
	h.pending = nil
	h.announced = &change
	if err := writeAssignmentChange(h.changePath, change); err != nil {
		log.Printf("persist assignment change: %v", err)
	}
}

func readAssignmentChange(path string) *assignmentChange {
	document, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var change assignmentChange
	if json.Unmarshal(document, &change) != nil || change.Generation == "" {
		return nil
	}
	return &change
}

func writeAssignmentChange(path string, change assignmentChange) error {
	document, err := json.Marshal(change)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(document, '\n'), 0o600)
}
