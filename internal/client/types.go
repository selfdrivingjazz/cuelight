package client

import (
	"encoding/json"
	"fmt"
)

// Problem is an RFC 9457 problem detail returned by the Cable API.
type Problem struct {
	Type          string         `json:"type"`
	Title         string         `json:"title"`
	Status        int            `json:"status"`
	Detail        string         `json:"detail,omitempty"`
	Instance      string         `json:"instance,omitempty"`
	CorrelationID string         `json:"correlationId,omitempty"`
	Extensions    map[string]any `json:"-"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return fmt.Sprintf("%s: %s", p.Title, p.Detail)
	}
	return p.Title
}

func (p Problem) MarshalJSON() ([]byte, error) {
	values := make(map[string]any, len(p.Extensions)+6)
	for key, value := range p.Extensions {
		values[key] = value
	}
	values["type"] = p.Type
	values["title"] = p.Title
	values["status"] = p.Status
	if p.Detail != "" {
		values["detail"] = p.Detail
	}
	if p.Instance != "" {
		values["instance"] = p.Instance
	}
	if p.CorrelationID != "" {
		values["correlationId"] = p.CorrelationID
	}
	return json.Marshal(values)
}

func (p *Problem) UnmarshalJSON(data []byte) error {
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if value, ok := values["type"].(string); ok {
		p.Type = value
	}
	if value, ok := values["title"].(string); ok {
		p.Title = value
	}
	if value, ok := values["status"].(float64); ok {
		p.Status = int(value)
	}
	if value, ok := values["detail"].(string); ok {
		p.Detail = value
	}
	if value, ok := values["instance"].(string); ok {
		p.Instance = value
	}
	if value, ok := values["correlationId"].(string); ok {
		p.CorrelationID = value
	}
	delete(values, "type")
	delete(values, "title")
	delete(values, "status")
	delete(values, "detail")
	delete(values, "instance")
	delete(values, "correlationId")
	p.Extensions = values
	return nil
}

type AdminToken struct {
	ID      string   `json:"id"`
	Prefix  string   `json:"prefix"`
	Scopes  []string `json:"scopes"`
	Revoked bool     `json:"revoked"`
}

type OneTimeToken struct {
	ID        string `json:"id"`
	Secret    string `json:"secret"`
	ExpiresAt string `json:"expiresAt"`
}

type RemoteLink struct {
	SessionID string `json:"sessionId"`
	RemoteURL string `json:"remoteUrl"`
	ExpiresAt string `json:"expiresAt"`
}

type Node struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	CredentialState    string `json:"credentialState"`
	DesiredGeneration  int64  `json:"desiredGeneration,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

type Upload struct {
	ID     string `json:"id"`
	Length int64  `json:"length"`
	Offset int64  `json:"offset"`
	State  string `json:"state"`
}

type Artifact struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

type Asset struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Artifact Artifact `json:"artifact"`
}

// AssignmentInput targets exactly one media item or playlist; control
// publishes or reuses the release that presents it.
type AssignmentInput struct {
	MediaID            string `json:"mediaId,omitempty"`
	PlaylistID         string `json:"playlistId,omitempty"`
	DeliveryMode       string `json:"deliveryMode,omitempty"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
	RemoteInput        bool   `json:"remoteInput"`
	ShowQR             bool   `json:"showQr"`
}

type Assignment struct {
	AssignmentInput
	ID                string `json:"id"`
	ReleaseID         string `json:"releaseId"`
	ReleaseDigest     string `json:"releaseDigest"`
	DesiredGeneration int64  `json:"desiredGeneration"`
}

// PageRequest is the common list query contract. Cursor remains opaque to the
// client; callers pass a server-issued value unchanged.
type PageRequest struct {
	Cursor  string
	Limit   int
	Sort    string
	Order   string
	Search  string
	Filters map[string]string
}

type PageInfo struct {
	NextCursor     *string `json:"nextCursor"`
	PreviousCursor *string `json:"previousCursor"`
	HasMore        bool    `json:"hasMore"`
}

type Page[T any] struct {
	Items []T      `json:"items"`
	Page  PageInfo `json:"page"`
}

type PageResponse[T any] = Page[T]

// Document represents an API object whose fields are intentionally open-ended.
type Document map[string]any

type OperatorMe struct {
	ID           string          `json:"id"`
	TokenID      string          `json:"tokenId,omitempty"`
	Scopes       []string        `json:"scopes"`
	Capabilities map[string]bool `json:"capabilities"`
}

// EventPage retains the sequence cursor at the top level for compatibility;
// Page carries the common envelope metadata for new callers.
type EventPage struct {
	Items      []Document `json:"items"`
	NextCursor *string    `json:"nextCursor"`
	Page       PageInfo   `json:"page"`
}
