package operatorapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/selfdrivingjazz/cuelight/internal/control/auth"
	"github.com/selfdrivingjazz/cuelight/internal/control/profile"
)

// ProfileService is implemented by profile.Service.
type ProfileService interface {
	List(context.Context) ([]profile.Profile, error)
	Get(context.Context, string) (profile.Profile, error)
	Create(ctx context.Context, actorID string, input profile.Input) (profile.Profile, error)
	Update(ctx context.Context, actorID, id string, expectedVersion int64, input profile.Input) (profile.Profile, error)
	Delete(context.Context, string) error
	Activate(ctx context.Context, actorID, idempotencyKey, id string) (profile.Activation, error)
	Active(context.Context) (profile.ActiveStatus, error)
}

type ProfileScreen struct {
	NodeID       string          `json:"nodeId"`
	MediaID      string          `json:"mediaId,omitempty"`
	PlaylistID   string          `json:"playlistId,omitempty"`
	DeliveryMode string          `json:"deliveryMode,omitempty"`
	RemoteInput  bool            `json:"remoteInput"`
	ShowQR       bool            `json:"showQr"`
	Settings     json.RawMessage `json:"settings,omitempty"`
}

type ProfileInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Screens     []ProfileScreen `json:"screens"`
}

type Profile struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	Version         int64           `json:"version"`
	Screens         []ProfileScreen `json:"screens"`
	CreatedBy       string          `json:"createdBy"`
	UpdatedBy       string          `json:"updatedBy"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
	LastActivatedAt *time.Time      `json:"lastActivatedAt,omitempty"`
	Active          bool            `json:"active"`
}

type ProfileList struct {
	Items []Profile `json:"items"`
}

type ProfileActivation struct {
	ID          string           `json:"id"`
	ProfileID   string           `json:"profileId"`
	ActivatedAt time.Time        `json:"activatedAt"`
	ActivatedBy string           `json:"activatedBy"`
	Results     []profile.Result `json:"results"`
}

type ProfileDrift struct {
	NodeID string `json:"nodeId"`
	Reason string `json:"reason"`
}

type ActiveProfile struct {
	ProfileID   string         `json:"profileId,omitempty"`
	ProfileName string         `json:"profileName,omitempty"`
	ActivatedAt *time.Time     `json:"activatedAt,omitempty"`
	Drifted     bool           `json:"drifted"`
	Drift       []ProfileDrift `json:"drift"`
}

func (h *Handler) profiles() (ProfileService, error) {
	if h.deps.Profiles == nil {
		return nil, errors.New("profile service is not configured")
	}
	return h.deps.Profiles, nil
}

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
	service, err := h.profiles()
	if err != nil {
		writeError(w, err)
		return
	}
	values, err := service.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	items := make([]Profile, 0, len(values))
	for _, value := range values {
		items = append(items, profileJSON(value))
	}
	writeJSON(w, http.StatusOK, ProfileList{Items: items})
}

func (h *Handler) createProfile(w http.ResponseWriter, r *http.Request) {
	service, err := h.profiles()
	if err != nil {
		writeError(w, err)
		return
	}
	var in ProfileInput
	if err := decodeJSON(w, r, h.jsonLimit(), &in); err != nil {
		writeError(w, err)
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	value, err := service.Create(r.Context(), actor.ID, profileInput(in))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", profileETag(value.Version))
	writeJSON(w, http.StatusCreated, profileJSON(value))
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	service, err := h.profiles()
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := service.Get(r.Context(), r.PathValue("profileId"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", profileETag(value.Version))
	writeJSON(w, http.StatusOK, profileJSON(value))
}

func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	service, err := h.profiles()
	if err != nil {
		writeError(w, err)
		return
	}
	version, err := parseProfileVersion(r.Header.Get("If-Match"))
	if err != nil {
		writeError(w, err)
		return
	}
	var in ProfileInput
	if err := decodeJSON(w, r, h.jsonLimit(), &in); err != nil {
		writeError(w, err)
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	value, err := service.Update(r.Context(), actor.ID, r.PathValue("profileId"), version, profileInput(in))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", profileETag(value.Version))
	writeJSON(w, http.StatusOK, profileJSON(value))
}

func (h *Handler) deleteProfile(w http.ResponseWriter, r *http.Request) {
	service, err := h.profiles()
	if err != nil {
		writeError(w, err)
		return
	}
	if err := service.Delete(r.Context(), r.PathValue("profileId")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) activateProfile(w http.ResponseWriter, r *http.Request) {
	service, err := h.profiles()
	if err != nil {
		writeError(w, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	value, err := service.Activate(r.Context(), actor.ID, key, r.PathValue("profileId"))
	if err != nil {
		writeError(w, err)
		return
	}
	results := value.Results
	if results == nil {
		results = []profile.Result{}
	}
	writeJSON(w, http.StatusOK, ProfileActivation{ID: value.ID, ProfileID: value.ProfileID, ActivatedAt: value.ActivatedAt.UTC(), ActivatedBy: value.ActivatedBy, Results: results})
}

func (h *Handler) getActiveProfile(w http.ResponseWriter, r *http.Request) {
	service, err := h.profiles()
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := service.Active(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := ActiveProfile{ProfileID: value.ProfileID, ProfileName: value.ProfileName, ActivatedAt: value.ActivatedAt, Drifted: value.Drifted, Drift: make([]ProfileDrift, 0, len(value.Drift))}
	for _, drift := range value.Drift {
		out.Drift = append(out.Drift, ProfileDrift{NodeID: drift.NodeID, Reason: drift.Reason})
	}
	writeJSON(w, http.StatusOK, out)
}

func profileETag(version int64) string { return fmt.Sprintf("\"version:%d\"", version) }

func parseProfileVersion(header string) (int64, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, ErrPrecondition
	}
	raw := strings.TrimPrefix(strings.TrimSuffix(strings.TrimPrefix(header, "W/"), "\""), "\"")
	raw = strings.TrimPrefix(raw, "version:")
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("%w: If-Match must carry the profile version", ErrInvalidRequest)
	}
	return version, nil
}

func profileInput(in ProfileInput) profile.Input {
	screens := make([]profile.Screen, 0, len(in.Screens))
	for _, s := range in.Screens {
		screens = append(screens, profile.Screen{NodeID: s.NodeID, MediaID: s.MediaID, PlaylistID: s.PlaylistID, DeliveryMode: s.DeliveryMode, RemoteInput: s.RemoteInput, ShowQR: s.ShowQR, Settings: s.Settings})
	}
	return profile.Input{Name: in.Name, Description: in.Description, Screens: screens}
}

func profileJSON(v profile.Profile) Profile {
	screens := make([]ProfileScreen, 0, len(v.Screens))
	for _, s := range v.Screens {
		screens = append(screens, ProfileScreen{NodeID: s.NodeID, MediaID: s.MediaID, PlaylistID: s.PlaylistID, DeliveryMode: s.DeliveryMode, RemoteInput: s.RemoteInput, ShowQR: s.ShowQR, Settings: s.Settings})
	}
	return Profile{ID: v.ID, Name: v.Name, Description: v.Description, Version: v.Version, Screens: screens, CreatedBy: v.CreatedBy, UpdatedBy: v.UpdatedBy, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, LastActivatedAt: v.LastActivatedAt, Active: v.Active}
}
