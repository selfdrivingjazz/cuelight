package pki

import (
	"bytes"
	"errors"
	"testing"
)

func TestAESGCMSealerEncryptsAndAuthenticatesResponse(t *testing.T) {
	sealer, err := NewAESGCMSealer(bytes.Repeat([]byte{0x2b}, AESGCMKeySize), &counterReader{})
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"secret":"one-time-secret"}`)
	additionalData := []byte("operation-and-idempotency-key")
	sealed, err := sealer.Seal(plaintext, additionalData)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plaintext) || bytes.Contains(sealed, []byte("one-time-secret")) {
		t.Fatal("sealed response contains plaintext")
	}
	opened, err := sealer.Open(sealed, additionalData)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("opened response = %q, want %q", opened, plaintext)
	}
	sealed[len(sealed)-1] ^= 0xff
	if _, err := sealer.Open(sealed, additionalData); !errors.Is(err, ErrInvalidSealedResponse) {
		t.Fatalf("tampered response error = %v, want ErrInvalidSealedResponse", err)
	}
}
