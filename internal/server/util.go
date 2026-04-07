package server

import (
	"crypto/rand"
	"fmt"
)

func generateRunID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%x", buf)
}
