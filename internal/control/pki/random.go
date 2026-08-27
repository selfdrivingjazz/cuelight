package pki

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
)

const randomSerialBytes = 20

type CryptoSerialSource struct {
	Reader io.Reader
}

func (source CryptoSerialSource) NextSerial(context.Context) (*big.Int, error) {
	reader := source.Reader
	if reader == nil {
		reader = rand.Reader
	}
	value := make([]byte, randomSerialBytes)
	if _, err := io.ReadFull(reader, value); err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	// RFC 5280 limits serials to 20 DER content octets. Keeping the high bit
	// clear prevents positive integers from requiring a 21st sign octet.
	value[0] &= 0x7f
	serial := new(big.Int).SetBytes(value)
	if serial.Sign() == 0 {
		value[len(value)-1] = 1
		serial.SetBytes(value)
	}
	return serial, nil
}

type RandomEpochSource struct {
	Reader io.Reader
}

func (source RandomEpochSource) NextEpoch(context.Context) (string, error) {
	reader := source.Reader
	if reader == nil {
		reader = rand.Reader
	}
	var value [16]byte
	if _, err := io.ReadFull(reader, value[:]); err != nil {
		return "", fmt.Errorf("generate event epoch: %w", err)
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return formatUUID(value), nil
}

func formatUUID(value [16]byte) string {
	var encoded [36]byte
	hex.Encode(encoded[0:8], value[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], value[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], value[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], value[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], value[10:16])
	return string(encoded[:])
}
