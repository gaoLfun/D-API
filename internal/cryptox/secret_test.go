package cryptox

import (
	"bytes"
	"testing"
)

func TestSecretBoxRoundTripAndTamper(t *testing.T) {
	box, err := NewSecretBox(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := box.Encrypt("sk-sensitive")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Decrypt(ciphertext)
	if err != nil || plain != "sk-sensitive" {
		t.Fatalf("round trip: plain=%q error=%v", plain, err)
	}
	ciphertext[len(ciphertext)-1] ^= 1
	if _, err := box.Decrypt(ciphertext); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}
