// Package secure encrypts sensitive data (like KYC document numbers) before it is stored.
package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// Cipher encrypts and decrypts with AES-256-GCM.
// GCM also detects tampering: a modified ciphertext fails to decrypt.
type Cipher struct {
	gcm cipher.AEAD
}

// NewCipher takes a 64-character hex key (32 bytes = AES-256).
func NewCipher(keyHex string) (*Cipher, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, errors.New("encryption key must be 64 hex characters (32 bytes)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{gcm: gcm}, nil
}

// Encrypt returns base64(nonce + ciphertext). A fresh random nonce every time
// means encrypting the same value twice gives different results.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := c.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt.
func (c *Cipher) Decrypt(encoded string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	n := c.gcm.NonceSize()
	if len(data) < n {
		return "", errors.New("ciphertext too short")
	}
	plain, err := c.gcm.Open(nil, data[:n], data[n:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plain), nil
}
