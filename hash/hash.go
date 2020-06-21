// Package hash implements 20-byte hashes as used by the BitTorrent protocol.
package hash

import (
	"encoding/base32"
	"encoding/hex"
)

// Hash is the type of 20-byte hashes
type Hash []byte

// A HashPair is a pair of hashes.
type HashPair struct {
	First  Hash
	Second Hash
}

func (hash Hash) String() string {
	if hash == nil {
		return "<nil>"
	}
	return hex.EncodeToString(hash)
}

func (hash Hash) Equal(h Hash) bool {
	if len(hash) != 20 || len(h) != 20 {
		panic("Hash has bad length")
	}
	for i := 0; i < 20; i++ {
		if hash[i] != h[i] {
			return false
		}
	}
	return true
}

// Parse handles both hex and base-32 strings.
func Parse(s string) Hash {
	h, err := hex.DecodeString(s)
	if err == nil && len(h) == 20 {
		return h
	}
	h, err = base32.StdEncoding.DecodeString(s)
	if err == nil && len(h) == 20 {
		return h
	}
	return nil
}
