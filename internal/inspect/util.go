package inspect

import (
	"crypto/sha256"
	"encoding/hex"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
