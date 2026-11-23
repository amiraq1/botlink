package store

import (
	"crypto/rand"
	"testing"
)

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

	aad := MemberAAD("bot_1")

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
	if _, err := DecryptToken(key, enc, MemberAAD("bot_2")); err == nil {
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

func TestMemberAADDecryptionRejectsDifferentMember(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	token := "4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d"

	enc1, err := EncryptToken(key, token, MemberAAD("bot_1"))
	if err != nil {
		t.Fatalf("encrypt bot_1: %v", err)
	}

	// Decrypting with bot_1 AAD succeeds
	dec1, err := DecryptToken(key, enc1, MemberAAD("bot_1"))
	if err != nil || dec1 != token {
		t.Fatalf("expected successful decryption for bot_1, got dec=%q err=%v", dec1, err)
	}

	// Attempting to decrypt bot_1's ciphertext with bot_2's AAD MUST fail
	if _, err := DecryptToken(key, enc1, MemberAAD("bot_2")); err == nil {
		t.Fatal("security violation: ciphertext of bot_1 successfully decrypted using bot_2's AAD")
	}
}
