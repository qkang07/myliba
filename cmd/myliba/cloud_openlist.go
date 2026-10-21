package main

import (
	"errors"
	"os"
	"strings"
)

// OpenList supplies the provider-specific Aliyun, Baidu, Quark, and other
// cloud-drive drivers. The gateway uses its authenticated WebDAV endpoint so
// one object protocol works across those drivers without storing credentials
// on the Hub or the local client.
type openListBackend struct{ *webDAVBackend }

func newOpenListBackend() (*openListBackend, error) {
	root, err := validBackendURL(strings.TrimSpace(os.Getenv("MYLIBA_OPENLIST_URL")))
	if err != nil {
		return nil, err
	}
	mount := strings.Trim(strings.TrimSpace(os.Getenv("MYLIBA_OPENLIST_MOUNT")), "/")
	if mount == "" || strings.Contains(mount, "\\") {
		return nil, errors.New("MYLIBA_OPENLIST_MOUNT must name a dedicated writable mount")
	}
	segments := strings.Split(mount, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return nil, errors.New("invalid OpenList mount path")
		}
	}
	if os.Getenv("MYLIBA_OPENLIST_USER") == "" {
		return nil, errors.New("MYLIBA_OPENLIST_USER is required")
	}
	// Path is unescaped here; url.URL.String escapes each segment once.
	root.Path = strings.TrimRight(root.Path, "/") + "/dav/" + strings.Join(segments, "/")
	backend, err := newWebDAVBackendAt(root, os.Getenv("MYLIBA_OPENLIST_USER"), os.Getenv("MYLIBA_OPENLIST_PASSWORD"))
	if err != nil {
		return nil, err
	}
	return &openListBackend{webDAVBackend: backend}, nil
}
