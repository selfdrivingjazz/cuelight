package operatorapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/cable"
	"github.com/jmill/cable/internal/control/page"
)

func (h *Handler) cableRepository() cable.Repository {
	if h.deps.CableRepository != nil {
		return h.deps.CableRepository
	}
	if h.deps.Cable != nil {
		return h.deps.Cable.Repository
	}
	return nil
}
func (h *Handler) cableProgrammingRepository() cable.ProgrammingRepository {
	if h.deps.Cable != nil && h.deps.Cable.Programming != nil {
		return h.deps.Cable.Programming
	}
	repository, _ := h.cableRepository().(cable.ProgrammingRepository)
	return repository
}

func cableActor(r *http.Request) auth.Actor {
	actor, _ := auth.ActorFromContext(r.Context())
	return actor
}
func cableMutation(r *http.Request, key string, expected int64) cable.Mutation {
	return cable.Mutation{ActorID: cableActor(r).ID, IdempotencyKey: key, ExpectedVersion: expected, Now: time.Now().UTC()}
}
func cablePageCursor(p cable.Page) (*string, *string) {
	var next, previous *string
	if p.NextCursor != "" {
		next = &p.NextCursor
	}
	if p.PreviousCursor != "" {
		previous = &p.PreviousCursor
	}
	return next, previous
}
func cablePageRequest(r *http.Request) (page.PageRequest, error) {
	request, err := page.Parse(r.URL.Query())
	if err != nil {
		return page.PageRequest{}, err
	}
	// Cable repositories expose keyset cursor and limit. Resource filters are
	// represented by path IDs, so silently ignoring arbitrary query filters is
	// not permitted.
	if request.Search != "" || request.Sort != "" || request.Order != "asc" || len(request.Filters) != 0 {
		return page.PageRequest{}, fmt.Errorf("%w: unsupported cable list filter", ErrInvalidRequest)
	}
	return request, nil
}
func cableIfMatch(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	if value == "" {
		return 0, ErrPrecondition
	}
	if !strings.HasPrefix(value, "\"v") || !strings.HasSuffix(value, "\"") {
		return 0, ErrPrecondition
	}
	version, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(value, "\"v"), "\""), 10, 64)
	if err != nil || version <= 0 {
		return 0, ErrPrecondition
	}
	return version, nil
}
func setCableETag(w http.ResponseWriter, version int64) {
	w.Header().Set("ETag", cable.ETag(version))
}

func (h *Handler) listCableDeployments(w http.ResponseWriter, r *http.Request) {
	repo := h.cableRepository()
	if repo == nil {
		writeError(w, errors.New("cable repository is not configured"))
		return
	}
	request, err := cablePageRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := repo.ListDeployments(r.Context(), request.Cursor, request.Limit)
	if err != nil {
		writeError(w, err)
		return
	}
	next, previous := cablePageCursor(result.Page)
	writeJSON(w, http.StatusOK, page.NewResponse(result.Items, next, previous, result.Page.HasMore))
}
func (h *Handler) createCableDeployment(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	var input cable.DeploymentInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.Cable.CreateDeployment(r.Context(), cableMutation(r, key, 0), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusCreated, result)
}
func (h *Handler) getCableDeployment(w http.ResponseWriter, r *http.Request) {
	repo := h.cableRepository()
	if repo == nil {
		writeError(w, errors.New("cable repository is not configured"))
		return
	}
	result, err := repo.Deployment(r.Context(), r.PathValue("deploymentId"))
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}
func (h *Handler) updateCableDeployment(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	var input cable.DeploymentInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.Cable.UpdateDeployment(r.Context(), cableMutation(r, "", version), r.PathValue("deploymentId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}
func (h *Handler) deleteCableDeployment(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	if err := h.deps.Cable.DeleteDeployment(r.Context(), cableMutation(r, "", version), r.PathValue("deploymentId")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listCableChannels(w http.ResponseWriter, r *http.Request) {
	repo := h.cableRepository()
	if repo == nil {
		writeError(w, errors.New("cable repository is not configured"))
		return
	}
	request, err := cablePageRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := repo.ListChannels(r.Context(), r.PathValue("deploymentId"), request.Cursor, request.Limit)
	if err != nil {
		writeError(w, err)
		return
	}
	next, previous := cablePageCursor(result.Page)
	writeJSON(w, http.StatusOK, page.NewResponse(result.Items, next, previous, result.Page.HasMore))
}
func (h *Handler) createCableChannel(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	var input cable.ChannelInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	input.DeploymentID = r.PathValue("deploymentId")
	result, err := h.deps.Cable.CreateChannel(r.Context(), cableMutation(r, key, 0), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusCreated, result)
}
func (h *Handler) getCableChannel(w http.ResponseWriter, r *http.Request) {
	repo := h.cableRepository()
	if repo == nil {
		writeError(w, errors.New("cable repository is not configured"))
		return
	}
	result, err := repo.Channel(r.Context(), r.PathValue("channelId"))
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}
func (h *Handler) updateCableChannel(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	var input cable.ChannelInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.Cable.UpdateChannel(r.Context(), cableMutation(r, "", version), r.PathValue("channelId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}
func (h *Handler) deleteCableChannel(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	if err := h.deps.Cable.DeleteChannel(r.Context(), cableMutation(r, "", version), r.PathValue("channelId")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listCablePrograms(w http.ResponseWriter, r *http.Request) {
	repo := h.cableRepository()
	if repo == nil {
		writeError(w, errors.New("cable repository is not configured"))
		return
	}
	request, err := cablePageRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := repo.ListProgramEntries(r.Context(), r.PathValue("channelId"), request.Cursor, request.Limit)
	if err != nil {
		writeError(w, err)
		return
	}
	next, previous := cablePageCursor(result.Page)
	writeJSON(w, http.StatusOK, page.NewResponse(result.Items, next, previous, result.Page.HasMore))
}
func (h *Handler) createCableProgram(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	var input cable.ProgramInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	input.ChannelID = r.PathValue("channelId")
	result, err := h.deps.Cable.CreateProgramEntry(r.Context(), cableMutation(r, key, 0), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusCreated, result)
}
func (h *Handler) getCableProgram(w http.ResponseWriter, r *http.Request) {
	repo := h.cableRepository()
	if repo == nil {
		writeError(w, errors.New("cable repository is not configured"))
		return
	}
	result, err := repo.ProgramEntry(r.Context(), r.PathValue("programId"))
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}
func (h *Handler) updateCableProgram(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	var input cable.ProgramInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.Cable.UpdateProgramEntry(r.Context(), cableMutation(r, "", version), r.PathValue("programId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}
func (h *Handler) deleteCableProgram(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("cable service is not configured"))
		return
	}
	if err := h.deps.Cable.DeleteProgramEntry(r.Context(), cableMutation(r, "", version), r.PathValue("programId")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) listCablePlaylists(w http.ResponseWriter, r *http.Request) {
	repository := h.cableProgrammingRepository()
	if repository == nil {
		writeError(w, errors.New("Cable programming repository is not configured"))
		return
	}
	request, err := cablePageRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := repository.ListPlaylists(r.Context(), request.Cursor, request.Limit)
	if err != nil {
		writeError(w, err)
		return
	}
	next, previous := cablePageCursor(result.Page)
	writeJSON(w, http.StatusOK, page.NewResponse(result.Items, next, previous, result.Page.HasMore))
}

func (h *Handler) createCablePlaylist(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("Cable service is not configured"))
		return
	}
	var input cable.PlaylistInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.Cable.CreatePlaylist(r.Context(), cableMutation(r, key, 0), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) getCablePlaylist(w http.ResponseWriter, r *http.Request) {
	repository := h.cableProgrammingRepository()
	if repository == nil {
		writeError(w, errors.New("Cable programming repository is not configured"))
		return
	}
	result, err := repository.Playlist(r.Context(), r.PathValue("playlistId"))
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listCableBlocks(w http.ResponseWriter, r *http.Request) {
	repository := h.cableProgrammingRepository()
	if repository == nil {
		writeError(w, errors.New("Cable programming repository is not configured"))
		return
	}
	request, err := cablePageRequest(r)
	if err != nil {
		writeError(w, err)
		return
	}
	result, err := repository.ListBlocks(r.Context(), r.PathValue("channelId"), request.Cursor, request.Limit)
	if err != nil {
		writeError(w, err)
		return
	}
	next, previous := cablePageCursor(result.Page)
	writeJSON(w, http.StatusOK, page.NewResponse(result.Items, next, previous, result.Page.HasMore))
}

func (h *Handler) createCableBlock(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("Cable service is not configured"))
		return
	}
	var input cable.BlockInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	input.ChannelID = r.PathValue("channelId")
	result, err := h.deps.Cable.CreateBlock(r.Context(), cableMutation(r, key, 0), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusCreated, result)
}
func (h *Handler) getCableBlock(w http.ResponseWriter, r *http.Request) {
	repository := h.cableProgrammingRepository()
	if repository == nil {
		writeError(w, errors.New("Cable programming repository is not configured"))
		return
	}
	detail, ok := repository.(cable.ProgrammingDetailRepository)
	if !ok {
		writeError(w, errors.New("Cable block detail repository is not configured"))
		return
	}
	result, err := detail.Block(r.Context(), r.PathValue("blockId"))
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) updateCablePlaylist(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("Cable service is not configured"))
		return
	}
	var input cable.PlaylistInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.Cable.UpdatePlaylist(r.Context(), cableMutation(r, "", version), r.PathValue("playlistId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) updateCableBlock(w http.ResponseWriter, r *http.Request) {
	version, err := cableIfMatch(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Cable == nil {
		writeError(w, errors.New("Cable service is not configured"))
		return
	}
	var input cable.BlockInput
	if err := decodeJSON(w, r, h.jsonLimit(), &input); err != nil {
		writeError(w, err)
		return
	}
	result, err := h.deps.Cable.UpdateBlock(r.Context(), cableMutation(r, "", version), r.PathValue("blockId"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	setCableETag(w, result.Version)
	writeJSON(w, http.StatusOK, result)
}
