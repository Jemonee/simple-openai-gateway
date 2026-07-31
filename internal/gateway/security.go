package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

type SecretBox struct {
	aead cipher.AEAD
}

func NewSecretBox(encodedKey string) (*SecretBox, error) {
	keyText := strings.TrimSpace(encodedKey)
	if keyText == "" {
		return nil, errors.New("GATEWAY_MASTER_KEY is required")
	}
	key, err := base64.StdEncoding.DecodeString(keyText)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(keyText)
	}
	if err != nil || len(key) != 32 {
		return nil, errors.New("GATEWAY_MASTER_KEY must be a base64-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretBox{aead: aead}, nil
}

func (box *SecretBox) Encrypt(value string) (string, error) {
	if box == nil || box.aead == nil {
		return "", errors.New("secret box is not initialized")
	}
	nonce := make([]byte, box.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := box.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (box *SecretBox) Decrypt(value string) (string, error) {
	if box == nil || box.aead == nil {
		return "", errors.New("secret box is not initialized")
	}
	sealed, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	nonceSize := box.aead.NonceSize()
	if len(sealed) < nonceSize {
		return "", errors.New("encrypted value is malformed")
	}
	plain, err := box.aead.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plain), nil
}

func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func generateSecret(prefix string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

func visibleKeyPrefix(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:8] + "..." + value[len(value)-4:]
}
