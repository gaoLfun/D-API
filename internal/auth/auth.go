package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func HashPassword(password string) ([]byte, error) {
	if len(password) < 12 {
		return nil, errors.New("password must be at least 12 characters")
	}
	return bcrypt.GenerateFromPassword([]byte(password), 12)
}

func CheckPassword(hash []byte, password string) bool {
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

func NewSessionToken() (string, []byte, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", nil, err
	}
	return token, HashToken(token), nil
}

func NewAPIKey() (string, string, []byte, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", "", nil, err
	}
	key := "dapi_" + raw
	return key, key[:14], HashToken(key), nil
}

func HashToken(token string) []byte {
	hash := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hash[:]
}

func randomToken(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
