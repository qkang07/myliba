//go:build ignore

// This file is included only by the explicit file-list build in
// scripts/build-storage.ps1 or scripts/build-storage.sh. The resulting
// storage-only binary has no SQLite or web UI dependency.
package main

import (
	"crypto/subtle"
	"encoding/json"
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
