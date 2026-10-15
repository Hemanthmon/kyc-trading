package secure

import (
	"strings"
	"testing"
)

const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestEncryptDecrypt(t *testing.T) {
	c, err := NewCipher(testKey)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := c.Encrypt("ABCDE1234F")
	b, _ := c.Encrypt("ABCDE1234F")
	if a == b {
		t.Error("same input should encrypt differently each time (random nonce)")
	}
	if strings.Contains(a, "ABCDE1234F") {
		t.Error("ciphertext must not contain the plaintext")
	}

	got, err := c.Decrypt(a)
	if err != nil || got != "ABCDE1234F" {
		t.Fatalf("decrypt = %q, %v", got, err)
	}
}

func TestTamperingDetected(t *testing.T) {
	c, _ := NewCipher(testKey)
	enc, _ := c.Encrypt("secret")

	tampered := []byte(enc)
	tampered[len(tampered)-3] ^= 1 // flip one bit
	if _, err := c.Decrypt(string(tampered)); err == nil {
		t.Error("tampered ciphertext should fail to decrypt")
	}
}

func TestBadKey(t *testing.T) {
	if _, err := NewCipher("tooshort"); err == nil {
		t.Error("expected error for short key")
	}
}
