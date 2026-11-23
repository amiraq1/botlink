package store

import (
	"crypto/rand"
	"strings"
	"testing"
)

func TestCiphertextVersionPrefix(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	token := "4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d"
	aad := MemberAAD(1, "bot_1")

	enc, err := EncryptToken(key, token, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "v1:") {
		t.Fatalf("expected ciphertext to have 'v1:' prefix, got: %s", enc)
	}

	// Decryption succeeds with v1: prefix
	dec, err := DecryptToken(key, enc, aad)
	if err != nil || dec != token {
		t.Fatalf("expected successful decrypt, got dec=%q err=%v", dec, err)
	}

	// Stripping prefix must cause decryption to fail
	rawCiphertext := strings.TrimPrefix(enc, "v1:")
	if _, err := DecryptToken(key, rawCiphertext, aad); err != ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed for ciphertext without prefix, got: %v", err)
	}

	// Unknown version prefix must fail
	if _, err := DecryptToken(key, "v2:"+rawCiphertext, aad); err != ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed for unknown version prefix, got: %v", err)
	}
}

func TestTokenCrypto(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}

	token := "4a5b6c7d8e9f0123456789abcdef0123"

	// Hashing
	h1 := HashToken(token)
	h2 := HashToken(token)
	if h1 != h2 {
		t.Fatalf("hashes not deterministic: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("expected 64 hex chars, got %d", len(h1))
	}

	aad := MemberAAD(1, "bot_1")

	// Encryption & Decryption
	enc, err := EncryptToken(key, token, aad)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == token {
		t.Fatal("ciphertext equals plaintext")
	}

	dec, err := DecryptToken(key, enc, aad)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != token {
		t.Fatalf("expected %q, got %q", token, dec)
	}

	// Mismatched AAD fails decryption
	if _, err := DecryptToken(key, enc, MemberAAD(1, "bot_2")); err == nil {
		t.Fatal("expected decryption error on mismatched AAD")
	}

	// Short key rejected
	if _, err := EncryptToken([]byte("short"), token, aad); err != ErrKeyTooShort {
		t.Fatalf("expected ErrKeyTooShort, got %v", err)
	}
	if _, err := DecryptToken([]byte("short"), enc, aad); err != ErrKeyTooShort {
		t.Fatalf("expected ErrKeyTooShort, got %v", err)
	}

	// Corrupted ciphertext rejected
	corrupted := enc[:len(enc)-4] + "dead"
	if _, err := DecryptToken(key, corrupted, aad); err == nil {
		t.Fatal("expected decryption error on corrupted ciphertext")
	}
}

func TestMemberAADGroupAndMemberIsolation(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	token := "4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d"

	// Encrypt for bot_1 in group 100
	encA, err := EncryptToken(key, token, MemberAAD(100, "bot_1"))
	if err != nil {
		t.Fatalf("encrypt group 100 bot_1: %v", err)
	}

	// Decrypting with matching group 100 and bot_1 succeeds
	decA, err := DecryptToken(key, encA, MemberAAD(100, "bot_1"))
	if err != nil || decA != token {
		t.Fatalf("expected successful decryption for group 100 bot_1, got dec=%q err=%v", decA, err)
	}

	// Attempting to decrypt group 100's ciphertext for the SAME bot_1 in group 200 MUST fail
	if _, err := DecryptToken(key, encA, MemberAAD(200, "bot_1")); err == nil {
		t.Fatal("security violation: ciphertext of group 100 bot_1 successfully decrypted using group 200 AAD")
	}

	// Attempting to decrypt group 100's ciphertext for bot_2 in group 100 MUST fail
	if _, err := DecryptToken(key, encA, MemberAAD(100, "bot_2")); err == nil {
		t.Fatal("security violation: ciphertext of group 100 bot_1 successfully decrypted using bot_2 AAD")
	}
}
