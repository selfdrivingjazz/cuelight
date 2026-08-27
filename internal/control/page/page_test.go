package page

import (
	"net/url"
	"strings"
	"testing"
)

func TestParseDefaultsAndBounds(t *testing.T) {
	r, err := Parse(url.Values{})
	if err != nil || r.Limit != DefaultLimit || r.Order != "asc" {
		t.Fatalf("defaults = %#v, %v", r, err)
	}
	for _, raw := range []string{"0", "-1", "101", "wat"} {
		if _, err := Parse(url.Values{"limit": []string{raw}}); err != ErrLimitInvalid {
			t.Fatalf("limit %q error = %v", raw, err)
		}
	}
}

func TestCodecRejectsTamperingAndWrongSecret(t *testing.T) {
	codec, err := NewCodec([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := codec.Encode(Position{Resource: "nodes", Sort: "createdAt", Order: "asc", Value: "2026-01-01T00:00:00Z", ID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(token)
	if err != nil || decoded.ID != "n1" {
		t.Fatalf("decode = %#v, %v", decoded, err)
	}
	parts := strings.Split(token, ".")
	parts[0] += "x"
	if _, err := codec.Decode(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered cursor accepted")
	}
	other, _ := NewCodec([]byte("fedcba9876543210"))
	if _, err := other.Decode(token); err == nil {
		t.Fatal("cursor signed by another secret accepted")
	}
}

func TestEventEnvelopeOmitsPreviousCursor(t *testing.T) {
	response := NewEventResponse([]string{"event"}, nil, false)
	if response.Page.PreviousCursor != nil || response.Page.HasMore {
		t.Fatalf("event envelope = %#v", response)
	}
}
