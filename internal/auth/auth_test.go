package auth

import (
	"bytes"
	"testing"
)

func TestPasswordAndTokens(t *testing.T) {
	hash, err := HashPassword("a-secure-password")
	if err != nil || !CheckPassword(hash, "a-secure-password") || CheckPassword(hash, "wrong-password") {
		t.Fatalf("password hash failed: %v", err)
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	key, prefix, keyHash, err := NewAPIKey()
	if err != nil || len(prefix) != 14 || !bytes.Equal(keyHash, HashToken(key)) {
		t.Fatalf("API key generation failed: %v", err)
	}
	other, _, _, _ := NewAPIKey()
	if bytes.Equal(HashToken(key), HashToken(other)) {
		t.Fatal("generated API keys collide")
	}
}
