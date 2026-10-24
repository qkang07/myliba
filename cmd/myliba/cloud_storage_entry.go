//go:build ignore

// This file is included only by the explicit file-list build in
// scripts/build-storage.ps1 or scripts/build-storage.sh. The resulting
// storage-only binary has no SQLite or web UI dependency.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
)

func main() {
	if err := runCloudStorage(); err != nil {
		log.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func safeEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

type iceServerConfig struct {
	URLs []string `json:"urls"`
}

// Hub signaling is not served by the storage-only binary.
func loadICEServers() ([]iceServerConfig, error) {
	return nil, errors.New("ICE unavailable in storage-only binary")
}

func (c *cloudRuntime) p2pRequest(context.Context, string, string, string, string, io.Reader) (*http.Response, error) {
	return nil, errors.New("Go storage-only node does not initiate WebRTC")
}
