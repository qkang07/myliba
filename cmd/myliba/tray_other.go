//go:build !windows

package main

import "net/http"

func runWindowsTray(_ *http.Server, _ *hubProfiles) (bool, error) {
	return false, nil
}
