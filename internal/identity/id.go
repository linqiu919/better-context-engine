package identity

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
)

func NewID(prefix string) string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return prefix + "_" + hex.EncodeToString(raw[:])
}

func NewToken(bytes int) string {
	raw := make([]byte, bytes)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}
