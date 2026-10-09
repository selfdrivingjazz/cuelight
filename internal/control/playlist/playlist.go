// Package playlist defines the library's ordered playlists: file or web media
// shown for a fixed duration each. Assigning a playlist to a screen publishes a
// media-schedule release that loops it.
package playlist

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/page"
)

var (
	ErrNotFound        = errors.New("playlist not found")
	ErrConflict        = errors.New("playlist version conflict")
	ErrNameConflict    = errors.New("a playlist with this name exists")
	ErrInvalidArgument = errors.New("invalid playlist argument")
)

func ETag(version int64) string { return fmt.Sprintf("\"v%d\"", version) }

type Mutation struct {
	ActorID         string
	IdempotencyKey  string
	ExpectedVersion int64
	Now             time.Time
}

type Playlist struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Items       []Item    `json:"items"`
	Version     int64     `json:"version"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Item is one entry. MediaID names the media item shown (file items resolve to
// their file media row when one exists); AssetID is set for file items only.
type Item struct {
	ID              string `json:"id"`
	PlaylistID      string `json:"playlistId"`
	MediaID         string `json:"mediaId,omitempty"`
	AssetID         string `json:"assetId,omitempty"`
	Position        int    `json:"position"`
	DurationSeconds int    `json:"durationSeconds"`
	Title           string `json:"title"`
	Subtitle        string `json:"subtitle"`
}

type Input struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Items       []ItemInput `json:"items"`
}

// ItemInput names exactly one source: a media item (file or web) or a file
// asset.
type ItemInput struct {
	MediaID         string `json:"mediaId"`
	AssetID         string `json:"assetId"`
	Position        int    `json:"position"`
	DurationSeconds int    `json:"durationSeconds"`
	Title           string `json:"title"`
	Subtitle        string `json:"subtitle"`
}

type Page struct {
	Items      []Playlist
	NextCursor string
	HasMore    bool
}

type Repository interface {
	CreatePlaylist(context.Context, Mutation, Input) (Playlist, error)
	Playlist(context.Context, string) (Playlist, error)
	ListPlaylists(ctx context.Context, cursor string, limit int) (Page, error)
	UpdatePlaylist(ctx context.Context, mutation Mutation, id string, input Input) (Playlist, error)
}

// Normalize trims text and validates positions, durations and sources.
func Normalize(input Input) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" {
		return Input{}, fmt.Errorf("%w: name is required", ErrInvalidArgument)
	}
	if len(input.Items) == 0 {
		return Input{}, fmt.Errorf("%w: at least one item is required", ErrInvalidArgument)
	}
	positions := make(map[int]bool, len(input.Items))
	items := make([]ItemInput, len(input.Items))
	for index, item := range input.Items {
		item.MediaID, item.AssetID = strings.TrimSpace(item.MediaID), strings.TrimSpace(item.AssetID)
		item.Title, item.Subtitle = strings.TrimSpace(item.Title), strings.TrimSpace(item.Subtitle)
		if (item.AssetID == "") == (item.MediaID == "") {
			return Input{}, fmt.Errorf("%w: item %d must name exactly one of mediaId or assetId", ErrInvalidArgument, index+1)
		}
		if item.Position <= 0 || positions[item.Position] {
			return Input{}, fmt.Errorf("%w: item %d needs a unique positive position", ErrInvalidArgument, index+1)
		}
		if item.DurationSeconds <= 0 {
			return Input{}, fmt.Errorf("%w: item %d needs a positive durationSeconds", ErrInvalidArgument, index+1)
		}
		positions[item.Position] = true
		items[index] = item
	}
	input.Items = items
	return input, nil
}

// List reads one keyset page of playlists for an HTTP query. Only cursor and
// limit are supported.
func List(ctx context.Context, repository Repository, values url.Values) (page.Response[Playlist], error) {
	request, err := page.Parse(values)
	if err != nil {
		return page.Response[Playlist]{}, err
	}
	if request.Search != "" || request.Sort != "" || request.Order != "asc" || len(request.Filters) != 0 {
		return page.Response[Playlist]{}, fmt.Errorf("%w: playlists support only cursor and limit", ErrInvalidArgument)
	}
	result, err := repository.ListPlaylists(ctx, request.Cursor, request.Limit)
	if err != nil {
		return page.Response[Playlist]{}, err
	}
	var next *string
	if result.NextCursor != "" {
		next = &result.NextCursor
	}
	return page.NewResponse(result.Items, next, nil, result.HasMore), nil
}
