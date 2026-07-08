package pgredis

import (
	"crypto/rand"
	"encoding/hex"
)

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
