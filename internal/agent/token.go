package agent

import (
	"crypto/rand"
	"encoding/hex"
)

// RandomToken generates a random hex token (bytes bytes of random entropy, hex-encoded).
// It is used to authenticate the write endpoint after --enable-write, avoiding plaintext/hardcoding.
func RandomToken(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic("failed to generate random numbers: " + err.Error())
	}
	return hex.EncodeToString(buf)
}
