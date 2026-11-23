package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrDecryptionFailed = errors.New("decryption failed")
	ErrKeyTooShort      = errors.New("encryption key must be at least 32 bytes")
)

// HashToken computes the SHA-256 hash of an agent token.
// The hash is stored in the database for O(1) indexed lookup without
// ever exposing the plain token in the table.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// MemberAAD returns the Additional Authenticated Data bound to a member's encrypted token,
// tying the ciphertext cryptographically to both the group ID and the member's bot ID.
func MemberAAD(groupID int64, botID string) []byte {
	return []byte(fmt.Sprintf("botlink:member:%d:%s", groupID, botID))
}

// EncryptToken encrypts the plain agent token with AES-256-GCM.
// A cryptographically random 12-byte nonce is generated for every encryption.
// The aad binds ciphertext to additional authenticated data (groupID and botID).
// Output is prefixed with the version tag "v1:".
func EncryptToken(key []byte, token string, aad []byte) (string, error) {
	if len(key) < 32 {
		return "", ErrKeyTooShort
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("rand nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, []byte(token), aad)
	return "v1:" + hex.EncodeToString(sealed), nil
}

// DecryptToken decrypts an AES-256-GCM hex-encoded token verifying aad.
// It requires the "v1:" version prefix.
func DecryptToken(key []byte, encHex string, aad []byte) (string, error) {
	if len(key) < 32 {
		return "", ErrKeyTooShort
	}
	if !strings.HasPrefix(encHex, "v1:") {
		return "", ErrDecryptionFailed
	}
	payload := strings.TrimPrefix(encHex, "v1:")
	data, err := hex.DecodeString(payload)
	if err != nil {
		return "", ErrDecryptionFailed
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("gcm: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return "", ErrDecryptionFailed
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return "", ErrDecryptionFailed
	}
	return string(plaintext), nil
}
