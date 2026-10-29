package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCloudEncryptedRangeCrossesBlocks(t *testing.T) {
	dir := t.TempDir()
	runtime := testCloudRuntime(t, filepath.Join(dir, "cloud"), "")
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, cloudChunkSize+4096)
	for i := range data {
		data[i] = byte((i*37 + 17) % 251)
	}
	path := filepath.Join(dir, "cloud.bin")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = client.uploadFile(context.Background(), "range-item", path, false); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	start, end := cloudChunkSize-128, cloudChunkSize+255
	request := httptest.NewRequest(http.MethodGet, "/api/cloud/files/range-item/file", nil)
	request.SetPathValue("id", "range-item")
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	response := httptest.NewRecorder()
	client.download(response, request)
	if response.Code != http.StatusPartialContent || !bytes.Equal(response.Body.Bytes(), data[start:end+1]) {
		t.Fatalf("range status=%d length=%d", response.Code, response.Body.Len())
	}
	if got := response.Header().Get("Content-Range"); got != fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)) {
		t.Fatalf("content range %q", got)
	}
}
