package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

type cloudRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cloudRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebDAVVirtualStorage(t *testing.T) {
	objects := map[string][]byte{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Basic dXNlcjpwYXNz" {
			w.WriteHeader(401)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/vault/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(201)
		case "PUT":
			data, _ := io.ReadAll(r.Body)
			objects[key] = data
			w.WriteHeader(201)
		case "GET":
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(data)
		case "PROPFIND":
			if r.Header.Get("Depth") != "1" {
				w.WriteHeader(400)
				return
			}
			w.WriteHeader(207)
			w.Write([]byte(`<d:multistatus xmlns:d="DAV:">`))
			for object := range objects {
				if strings.HasPrefix(object, key) {
					fmt.Fprintf(w, `<d:response><d:href>/vault/%s</d:href></d:response>`, object)
				}
			}
			w.Write([]byte(`</d:multistatus>`))
		}
	}))
	defer server.Close()
	t.Setenv("MYLIBA_WEBDAV_URL", server.URL+"/vault")
	t.Setenv("MYLIBA_WEBDAV_USER", "user")
	t.Setenv("MYLIBA_WEBDAV_PASSWORD", "pass")
	backend, err := newWebDAVBackend()
	if err != nil {
		t.Fatal(err)
	}
	disk, err := newCloudDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	disk.backend = backend
	data := []byte("encrypted-block")
	id := cloudHash(data)
	if err := disk.put("blocks", id, bytes.NewReader(data), cloudMaxBlobSize); err != nil {
		t.Fatal(err)
	}
	ids, err := disk.list("blocks")
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("inventory: %v %v", ids, err)
	}
	file, err := disk.get("blocks", id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get: %q %v", got, err)
	}
	if err := disk.put("blocks", id, bytes.NewReader([]byte("tampered")), cloudMaxBlobSize); err == nil {
		t.Fatal("accepted bad hash")
	}
}

func TestS3VirtualStorage(t *testing.T) {
	objects := map[string][]byte{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=access/") ||
			r.Header.Get("x-amz-content-sha256") == "" || r.Header.Get("x-amz-date") == "" {
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Query().Get("list-type") == "2" {
			type entry struct {
				Key string `xml:"Key"`
			}
			var result struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []entry  `xml:"Contents"`
			}
			for key := range objects {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					result.Contents = append(result.Contents, entry{key})
				}
			}
			sort.Slice(result.Contents, func(i, j int) bool { return result.Contents[i].Key < result.Contents[j].Key })
			w.WriteHeader(200)
			xml.NewEncoder(w).Encode(result)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/bucket/")
		switch r.Method {
		case "PUT":
			data, _ := io.ReadAll(r.Body)
			if r.Header.Get("x-amz-content-sha256") != cloudHash(data) {
				w.WriteHeader(403)
				return
			}
			objects[key] = data
			w.WriteHeader(200)
		case "GET":
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.Write(data)
		}
	}))
	defer server.Close()
	t.Setenv("MYLIBA_S3_ENDPOINT", server.URL)
	t.Setenv("MYLIBA_S3_BUCKET", "bucket")
	t.Setenv("MYLIBA_S3_PREFIX", "myliba")
	t.Setenv("MYLIBA_S3_REGION", "us-east-1")
	t.Setenv("MYLIBA_S3_ACCESS_KEY_ID", "access")
	t.Setenv("MYLIBA_S3_SECRET_ACCESS_KEY", "secret")
	backend, err := newS3Backend()
	if err != nil {
		t.Fatal(err)
	}
	disk, err := newCloudDisk(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	disk.backend = backend
	data := []byte("opaque-ciphertext")
	id := cloudHash(data)
	if err := disk.put("blocks", id, bytes.NewReader(data), cloudMaxBlobSize); err != nil {
		t.Fatal(err)
	}
	ids, err := disk.list("blocks")
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatalf("inventory: %v %v", ids, err)
	}
	file, err := disk.get("blocks", id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get: %q %v", got, err)
	}
}

func TestS3VirtualHostedAddressingForOSS(t *testing.T) {
	t.Setenv("MYLIBA_S3_ENDPOINT", "https://oss-cn-hangzhou.aliyuncs.com")
	t.Setenv("MYLIBA_S3_BUCKET", "private-library")
	t.Setenv("MYLIBA_S3_REGION", "cn-hangzhou")
	t.Setenv("MYLIBA_S3_ACCESS_KEY_ID", "access")
	t.Setenv("MYLIBA_S3_SECRET_ACCESS_KEY", "secret")
	t.Setenv("MYLIBA_S3_ADDRESSING", "virtual")
	backend, err := newS3Backend()
	if err != nil {
		t.Fatal(err)
	}
	backend.client.Transport = cloudRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "private-library.oss-cn-hangzhou.aliyuncs.com" || r.URL.Path != "/blocks/example" {
			t.Errorf("unexpected OSS URL: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	if err := backend.Put("blocks", "example", []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
}
