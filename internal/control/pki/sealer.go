package pki

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const AESGCMKeySize = 32

type AESGCMSealer struct {
	aead   cipher.AEAD
	random io.Reader
}

func NewAESGCMSealer(key []byte, random io.Reader) (*AESGCMSealer, error) {
	if len(key) != AESGCMKeySize {
		return nil, fmt.Errorf("%w: AES-GCM key must be 32 bytes", ErrInvalidInput)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create response sealer: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create response sealer: %w", err)
	}
	if random == nil {
		random = rand.Reader
	}
	return &AESGCMSealer{aead: aead, random: random}, nil
}

func (sealer *AESGCMSealer) Seal(plaintext, additionalData []byte) ([]byte, error) {
	nonce := make([]byte, sealer.aead.NonceSize())
	if _, err := io.ReadFull(sealer.random, nonce); err != nil {
		return nil, fmt.Errorf("seal idempotency response: %w", err)
	}
	return sealer.aead.Seal(nonce, nonce, plaintext, additionalData), nil
}

func (sealer *AESGCMSealer) Open(sealed, additionalData []byte) ([]byte, error) {
	nonceSize := sealer.aead.NonceSize()
	if len(sealed) < nonceSize+sealer.aead.Overhead() {
		return nil, ErrInvalidSealedResponse
	}
	plaintext, err := sealer.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], additionalData)
	if err != nil {
		return nil, ErrInvalidSealedResponse
	}
	return plaintext, nil
}
