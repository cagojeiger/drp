package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

const (
	salt       = "frp"
	iterations = 64
)

// DeriveKey derives AES-128 key from token using PBKDF2.
// frp spec: PBKDF2(token, salt="frp", iter=64, keyLen=aes.BlockSize, sha1)
func DeriveKey(token string) []byte {
	return pbkdf2.Key([]byte(token), []byte(salt), iterations, aes.BlockSize, sha1.New)
}

// NewCryptoWriter returns a writer that encrypts data with AES-128-CFB.
// Writes a random IV as the first aes.BlockSize bytes.
func NewCryptoWriter(w io.Writer, key []byte) (io.Writer, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("rand.Read: %w", err)
	}

	if _, err := w.Write(iv); err != nil {
		return nil, fmt.Errorf("write IV: %w", err)
	}

	stream := cipher.NewCFBEncrypter(block, iv)
	return &cipher.StreamWriter{S: stream, W: w}, nil
}

// NewCryptoReader returns a reader that decrypts AES-128-CFB data.
// Reads the first aes.BlockSize bytes as IV.
func NewCryptoReader(r io.Reader, key []byte) (io.Reader, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}

	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(r, iv); err != nil {
		return nil, fmt.Errorf("read IV: %w", err)
	}

	stream := cipher.NewCFBDecrypter(block, iv)
	return &cipher.StreamReader{S: stream, R: r}, nil
}
