package auth

import (
	"crypto/md5"
	"crypto/subtle"
	"fmt"
)

// BuildAuthKey computes frp auth key: MD5(token + timestamp).
func BuildAuthKey(token string, timestamp int64) string {
	raw := fmt.Sprintf("%s%d", token, timestamp)
	return fmt.Sprintf("%x", md5.Sum([]byte(raw)))
}

// VerifyAuth checks client's privilege_key using constant-time comparison.
func VerifyAuth(serverToken string, timestamp int64, clientKey string) bool {
	expected := BuildAuthKey(serverToken, timestamp)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(clientKey)) == 1
}
