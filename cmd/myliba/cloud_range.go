package main

import (
	"context"
	"crypto/cipher"
	"errors"
	"io"
	"sort"
)

// cloudRangeReader decrypts only the encrypted blocks touched by a seek.
type cloudRangeReader struct {
	ctx         context.Context
	client      *cloudClient
	record      cloudRecord
	manifest    cloudManifest
	aead        cipher.AEAD
	starts      []int64
	position    int64
	cachedIndex int
	cached      []byte
}

func newCloudRangeReader(ctx context.Context, client *cloudClient, record cloudRecord, manifest cloudManifest, aead cipher.AEAD) (*cloudRangeReader, error) {
	reader := &cloudRangeReader{ctx: ctx, client: client, record: record, manifest: manifest, aead: aead, cachedIndex: -1}
	var total int64
	for _, chunk := range manifest.Chunks {
		if chunk.Size <= 0 || int64(chunk.Size) > manifest.Size-total {
			return nil, errors.New("invalid cloud chunk sizes")
		}
		reader.starts = append(reader.starts, total)
		total += int64(chunk.Size)
	}
	if total != manifest.Size {
		return nil, errors.New("invalid cloud file size")
	}
	return reader, nil
}

func (r *cloudRangeReader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = r.position
	case io.SeekEnd:
		base = r.manifest.Size
	default:
		return 0, errors.New("invalid seek")
	}
	if offset < -base || offset > int64(^uint64(0)>>1)-base {
		return 0, errors.New("invalid seek")
	}
	r.position = base + offset
	return r.position, nil
}

func (r *cloudRangeReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.position >= r.manifest.Size {
		return 0, io.EOF
	}
	index := sort.Search(len(r.starts), func(i int) bool { return r.starts[i] > r.position }) - 1
	if index < 0 {
		return 0, errors.New("invalid cloud position")
	}
	if r.cachedIndex != index {
		chunk := r.manifest.Chunks[index]
		data, err := r.client.fetchBlock(r.ctx, chunk.ID, index)
		if err != nil {
			return 0, err
		}
		plain, err := r.aead.Open(nil, cloudNonce(index), data, cloudAAD(r.record, index, chunk.Size))
		if err != nil || len(plain) != chunk.Size {
			return 0, errors.New("cloud block verification failed")
		}
		r.cached, r.cachedIndex = plain, index
	}
	n := copy(p, r.cached[int(r.position-r.starts[index]):])
	r.position += int64(n)
	return n, nil
}
