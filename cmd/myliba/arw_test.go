package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestARWEmbeddedPreview(t *testing.T) {
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 24, 16))
	img.Set(0, 0, color.White)
	if err := jpeg.Encode(&encoded, img, nil); err != nil {
		t.Fatal(err)
	}
	// IFD0 points to a sub-IFD containing a JPEG interchange stream.
	header := make([]byte, 56)
	copy(header, []byte{'I', 'I', 42, 0, 8, 0, 0, 0})
	binary.LittleEndian.PutUint16(header[8:], 1)
	binary.LittleEndian.PutUint16(header[10:], 0x14a)
	binary.LittleEndian.PutUint16(header[12:], 4)
	binary.LittleEndian.PutUint32(header[14:], 1)
	binary.LittleEndian.PutUint32(header[18:], 26)
	binary.LittleEndian.PutUint16(header[26:], 2)
	binary.LittleEndian.PutUint16(header[28:], 0x201)
	binary.LittleEndian.PutUint16(header[30:], 4)
	binary.LittleEndian.PutUint32(header[32:], 1)
	binary.LittleEndian.PutUint32(header[36:], 56)
	binary.LittleEndian.PutUint16(header[40:], 0x202)
	binary.LittleEndian.PutUint16(header[42:], 4)
	binary.LittleEndian.PutUint32(header[44:], 1)
	binary.LittleEndian.PutUint32(header[48:], uint32(encoded.Len()))
	path := filepath.Join(t.TempDir(), "photo.ARW")
	if err := os.WriteFile(path, append(header, encoded.Bytes()...), 0600); err != nil {
		t.Fatal(err)
	}
	if fileKind(path) != "image" {
		t.Fatal("ARW not classified as image")
	}
	result, err := makeImageThumbnail(path, 12, 12)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(result))
	if err != nil || cfg.Width != 12 || cfg.Height != 8 {
		t.Fatalf("unexpected thumbnail: %+v %v", cfg, err)
	}
	binary.LittleEndian.PutUint32(header[48:], 0xffffffff)
	if err := os.WriteFile(path, header, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := makeImageThumbnail(path, 12, 12); err == nil {
		t.Fatal("invalid preview accepted")
	}
}
