package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

// generatedTokenPrefix matches what OpenAI-compatible clients expect to see.
// Authentication compares sha256 hashes, so the prefix never participates in
// verification and keys issued under an older prefix keep working.
const generatedTokenPrefix = "sk-"

// GeneratedKey contains the only plaintext copy of a newly generated Gateway
// token. Callers must persist Hash and discard Token after returning it once.
type GeneratedKey struct {
	Token  string
	Prefix string
	Hash   string
}

func GenerateKey() (GeneratedKey, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return GeneratedKey{}, errors.New("generate gateway key failed")
	}
	token := generatedTokenPrefix + base64.RawURLEncoding.EncodeToString(secret)
	prefixLength := 16
	if len(token) < prefixLength {
		prefixLength = len(token)
	}
	return GeneratedKey{Token: token, Prefix: token[:prefixLength], Hash: HashToken(token)}, nil
}

func HashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
