package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// A virtual storage node presents the same direct cloud protocol as a disk
// node. Only ciphertext and opaque catalog records reach the backing service.
type cloudObjectBackend interface {
	Put(kind, id string, data []byte) error
	Get(kind, id string) ([]byte, error)
	List(kind string) ([]string, error)
}

func newCloudBackendFromEnv() (cloudObjectBackend, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MYLIBA_CLOUD_BACKEND"))) {
	case "", "disk":
		return nil, nil
	case "webdav":
		return newWebDAVBackend()
	case "s3":
		return newS3Backend()
	default:
		return nil, errors.New("MYLIBA_CLOUD_BACKEND must be disk, webdav, or s3")
	}
}

func cloudBackendHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func validBackendURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid cloud backend URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return nil, errors.New("cloud backend requires HTTPS outside loopback")
	}
	return u, nil
}

type webDAVBackend struct {
	root   *url.URL
	user   string
	pass   string
	client *http.Client
}

func newWebDAVBackend() (*webDAVBackend, error) {
	root, err := validBackendURL(strings.TrimSpace(os.Getenv("MYLIBA_WEBDAV_URL")))
	if err != nil {
		return nil, err
	}
	backend := &webDAVBackend{root: root, user: os.Getenv("MYLIBA_WEBDAV_USER"), pass: os.Getenv("MYLIBA_WEBDAV_PASSWORD"), client: cloudBackendHTTPClient()}
	for _, kind := range []string{"blocks", "catalog"} {
		response, err := backend.request("MKCOL", kind+"/", nil, nil)
		if err != nil {
			return nil, err
		}
		response.Body.Close()
		if response.StatusCode != 201 && response.StatusCode != 405 {
			return nil, fmt.Errorf("WebDAV create %s: HTTP %d", kind, response.StatusCode)
		}
	}
	return backend, nil
}

func (b *webDAVBackend) objectURL(key string) string {
	u := *b.root
	u.Path = strings.TrimRight(b.root.Path, "/") + "/" + key
	return u.String()
}

func (b *webDAVBackend) request(method, key string, body []byte, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest(method, b.objectURL(key), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if b.user != "" {
		req.SetBasicAuth(b.user, b.pass)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return b.client.Do(req)
}

func (b *webDAVBackend) Put(kind, id string, data []byte) error {
	response, err := b.request(http.MethodPut, kind+"/"+id, data, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 201 && response.StatusCode != 204 {
		return fmt.Errorf("WebDAV PUT: HTTP %d", response.StatusCode)
	}
	return nil
}

func (b *webDAVBackend) Get(kind, id string) ([]byte, error) {
	response, err := b.request(http.MethodGet, kind+"/"+id, nil, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("WebDAV GET: HTTP %d", response.StatusCode)
	}
	limit := int64(cloudMaxBlobSize)
	if kind == "catalog" {
		limit = cloudMaxRecordSize
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("WebDAV object too large")
	}
	return data, nil
}

func (b *webDAVBackend) List(kind string) ([]string, error) {
	response, err := b.request("PROPFIND", kind+"/", []byte(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/></d:prop></d:propfind>`), map[string]string{"Depth": "1", "Content-Type": "application/xml"})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 207 {
		return nil, fmt.Errorf("WebDAV PROPFIND: HTTP %d", response.StatusCode)
	}
	var result struct {
		Responses []struct {
			Href string `xml:"href"`
		} `xml:"response"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return nil, err
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, item := range result.Responses {
		u, err := url.Parse(item.Href)
		if err != nil {
			continue
		}
		name := path.Base(strings.TrimRight(u.Path, "/"))
		if validCloudHash(name) && !seen[name] {
			ids = append(ids, name)
			seen[name] = true
		}
	}
	sort.Strings(ids)
	return ids, nil
}
