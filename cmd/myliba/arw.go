package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"os"
)

// arwPreview extracts the largest valid JPEG referenced by TIFF preview IFDs.
// It never decodes the RAW sensor data and bounds both metadata and image reads.
func arwPreview(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	read := func(off int64, n int) ([]byte, error) {
		if off < 0 || n < 0 || off > size || int64(n) > size-off {
			return nil, io.ErrUnexpectedEOF
		}
		b := make([]byte, n)
		_, e := f.ReadAt(b, off)
		return b, e
	}
	hdr, err := read(0, 8)
	if err != nil {
		return nil, err
	}
	var order binary.ByteOrder
	switch string(hdr[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("invalid ARW TIFF header")
	}
	if order.Uint16(hdr[2:4]) != 42 {
		return nil, fmt.Errorf("invalid ARW TIFF header")
	}
	queue := []uint32{order.Uint32(hdr[4:8])}
	seen := map[uint32]bool{}
	var best []byte
	var bestPixels int64
	for len(queue) > 0 && len(seen) < 128 {
		off := queue[0]
		queue = queue[1:]
		if off == 0 || seen[off] {
			continue
		}
		seen[off] = true
		countBytes, e := read(int64(off), 2)
		if e != nil {
			continue
		}
		count := int(order.Uint16(countBytes))
		if count > 4096 {
			continue
		}
		entries, e := read(int64(off)+2, count*12+4)
		if e != nil {
			continue
		}
		var jpegOffset, jpegLength uint32
		for i := 0; i < count; i++ {
			entry := entries[i*12 : i*12+12]
			tag := order.Uint16(entry)
			typ := order.Uint16(entry[2:])
			n := order.Uint32(entry[4:])
			value := order.Uint32(entry[8:])
			if typ != 4 || n == 0 {
				continue
			}
			switch tag {
			case 0x201:
				if n == 1 {
					jpegOffset = value
				}
			case 0x202:
				if n == 1 {
					jpegLength = value
				}
			case 0x14a, 0x8769, 0x8825:
				if n == 1 {
					queue = append(queue, value)
				} else if n <= 128 {
					b, e := read(int64(value), int(n)*4)
					if e == nil {
						for j := 0; j < int(n); j++ {
							queue = append(queue, order.Uint32(b[j*4:]))
						}
					}
				}
			}
		}
		queue = append(queue, order.Uint32(entries[count*12:]))
		if jpegLength < 4 || jpegLength > 32<<20 {
			continue
		}
		b, e := read(int64(jpegOffset), int(jpegLength))
		if e != nil || b[0] != 0xff || b[1] != 0xd8 {
			continue
		}
		cfg, format, e := image.DecodeConfig(bytes.NewReader(b))
		if e != nil || format != "jpeg" || cfg.Width < 1 || cfg.Height < 1 {
			continue
		}
		pixels := int64(cfg.Width) * int64(cfg.Height)
		if pixels > maxImagePixels {
			continue
		}
		if pixels > bestPixels {
			bestPixels = pixels
			best = b
		}
	}
	if best == nil {
		return nil, fmt.Errorf("ARW 文件不含可用的 JPEG 预览")
	}
	return best, nil
}
