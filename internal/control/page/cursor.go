package page

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

var ErrCursorTampered = errors.New("cursor signature is invalid")

var (
	defaultCodecOnce sync.Once
	defaultCodec     *Codec
)

// DefaultCodec provides a process-local signer for repositories that do not
// own application key configuration. Deployments should prefer NewCodec with a
// stable secret when cursors must survive process restarts.
func DefaultCodec() *Codec {
	defaultCodecOnce.Do(func() {
		secret := sha256.Sum256([]byte("cable-controller-pagination-v1"))
		defaultCodec, _ = NewCodec(secret[:])
	})
	return defaultCodec
}

// Position contains the keyset position encoded in an opaque cursor. Value is
// the serialized primary sort value and ID is the deterministic tie-breaker.
type Position struct {
	Resource string `json:"resource,omitempty"`

	Sort     string `json:"sort,omitempty"`
	Order    string `json:"order,omitempty"`
	Value    string `json:"value,omitempty"`
	ID       string `json:"id,omitempty"`
	Sequence int64  `json:"sequence,omitempty"`
}

type envelope struct {
	Version int      `json:"v"`
	Payload Position `json:"p"`
}

// Codec signs cursors so clients cannot alter keyset positions or use a cursor
// issued for a different resource/query. The secret must be stable for the
// lifetime of a controller instance (rotation invalidates old cursors).
type Codec struct{ secret []byte }

func NewCodec(secret []byte) (*Codec, error) {
	if len(secret) < 16 {
		return nil, errors.New("cursor secret must be at least 16 bytes")
	}
	return &Codec{secret: append([]byte(nil), secret...)}, nil
}

func (c *Codec) Encode(position Position) (string, error) {
	if c == nil || len(c.secret) == 0 {
		return "", ErrCursorInvalid
	}
	data, err := json.Marshal(envelope{Version: 1, Payload: position})
	if err != nil {
		return "", err
	}
	sig := c.sign(data)
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *Codec) Decode(token string) (Position, error) {
	if c == nil || len(c.secret) == 0 || token == "" || len(token) > 2048 {
		return Position{}, ErrCursorInvalid
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Position{}, ErrCursorInvalid
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Position{}, ErrCursorInvalid
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Position{}, ErrCursorInvalid
	}
	want := c.sign(data)
	if len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
		return Position{}, ErrCursorTampered
	}
	var value envelope
	if err := json.Unmarshal(data, &value); err != nil || value.Version != 1 {
		return Position{}, ErrCursorInvalid
	}
	return value.Payload, nil
}

func (c *Codec) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

type Cursor = Position

func EncodeCursor(position Position) (string, error) { return DefaultCodec().Encode(position) }
func DecodeCursor(token string) (Position, error)    { return DefaultCodec().Decode(token) }
