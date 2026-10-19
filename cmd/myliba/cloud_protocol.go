package main

import "sort"

// The unencrypted fields identify an opaque catalog record and its encrypted
// blocks. Names, content keys, and file metadata stay inside Sealed.
type cloudRecord struct {
	Format      int      `json:"format,omitempty"`
	KeepHistory bool     `json:"keepHistory,omitempty"`
	LibraryID   string   `json:"libraryId"`
	ObjectID    string   `json:"objectId"`
	RevisionID  string   `json:"revisionId"`
	AuthorID    string   `json:"authorId"`
	CreatedAt   string   `json:"createdAt"`
	Nonce       string   `json:"nonce"`
	Sealed      string   `json:"sealed"`
	BlockIDs    []string `json:"blockIds"`
}

func rankedCloudPeers(id string, peers []cloudNode) []cloudNode {
	result := append([]cloudNode(nil), peers...)
	sort.Slice(result, func(i, j int) bool {
		return cloudHash([]byte(id+result[i].ID)) < cloudHash([]byte(id+result[j].ID))
	})
	return result
}
