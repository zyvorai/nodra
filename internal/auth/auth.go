// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

func NewToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func Hash(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

func EqualHash(hash, token string) bool {
	a, err1 := hex.DecodeString(hash)
	bSum := sha256.Sum256([]byte(token))
	if err1 != nil || len(a) != len(bSum) {
		return false
	}
	return subtle.ConstantTimeCompare(a, bSum[:]) == 1
}

func EqualToken(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
