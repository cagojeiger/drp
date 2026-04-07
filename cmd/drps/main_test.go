package main

import (
	"net/http"
	"testing"
	"time"
)

func TestServerReadHeaderTimeout(t *testing.T) {
	srv := &http.Server{
		Addr:              ":0",
		ReadHeaderTimeout: 60 * time.Second,
	}
	if srv.ReadHeaderTimeout != 60*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want %v", srv.ReadHeaderTimeout, 60*time.Second)
	}
}
