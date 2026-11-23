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

	// Encryption & Decryption
	enc, err := EncryptToken(key, token)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if enc == token {
		t.Fatal("ciphertext equals plaintext")
	}

	dec, err := DecryptToken(key, enc)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if dec != token {
		t.Fatalf("expected %q, got %q", token, dec)
	}

	// Short key rejected
	if _, err := EncryptToken([]byte("short"), token); err != ErrKeyTooShort {
		t.Fatalf("expected ErrKeyTooShort, got %v", err)
	}
	if _, err := DecryptToken([]byte("short"), enc); err != ErrKeyTooShort {
		t.Fatalf("expected ErrKeyTooShort, got %v", err)
	}

	// Corrupted ciphertext rejected
	corrupted := enc[:len(enc)-4] + "dead"
	if _, err := DecryptToken(key, corrupted); err == nil {
		t.Fatal("expected decryption error on corrupted ciphertext")
	}
}
