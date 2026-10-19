package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
)

func (c *cloudRuntime) hasRemote(ctx context.Context, peer cloudNode, id, token string) bool {
	response, err := c.request(ctx, http.MethodHead, peer.Endpoint, "/cloud/blocks/"+id, token, nil)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusOK
}

// Every node reconciles its own inventory against the replicated catalog.
// The Hub supplies peers but never handles block bytes.
func (c *cloudRuntime) repairBlocks(ctx context.Context, token string, peers []cloudNode) {
	ids, err := c.disk.list("catalog")
	if err != nil {
		return
	}
	records := make([]cloudRecord, 0, len(ids))
	byObject := map[string][]cloudRecord{}
	for _, recordID := range ids {
		file, err := c.disk.get("catalog", recordID)
		if err != nil {
			continue
		}
		var record cloudRecord
		err = json.NewDecoder(io.LimitReader(file, cloudMaxRecordSize)).Decode(&record)
		file.Close()
		if err != nil || record.LibraryID == "" || record.ObjectID == "" {
			continue
		}
		records = append(records, record)
		key := record.LibraryID + "|" + record.ObjectID
		byObject[key] = append(byObject[key], record)
	}
	eligible := map[string]bool{}
	for key, versions := range byObject {
		sort.Slice(versions, func(i, j int) bool {
			if versions[i].CreatedAt == versions[j].CreatedAt {
				return versions[i].RevisionID > versions[j].RevisionID
			}
			return versions[i].CreatedAt > versions[j].CreatedAt
		})
		for index, record := range versions {
			if index > 0 && !record.KeepHistory {
				break
			}
			eligible[key+"|"+record.RevisionID] = true
			if !record.KeepHistory {
				break
			}
		}
	}
	all := append([]cloudNode(nil), peers...)
	if c.endpoint != "" {
		all = append(all, cloudNode{ID: c.id, Endpoint: c.endpoint, Role: c.role})
	}
	budget := 16
	for _, record := range records {
		if budget <= 0 {
			return
		}
		if !eligible[record.LibraryID+"|"+record.ObjectID+"|"+record.RevisionID] {
			continue
		}
		for _, id := range record.BlockIDs {
			if budget <= 0 {
				return
			}
			if !validCloudHash(id) {
				continue
			}
			ranked := rankedCloudPeers(id, all)
			targetCount := 3
			if len(ranked) < targetCount {
				targetCount = len(ranked)
			}
			selfTarget := false
			for _, node := range ranked[:targetCount] {
				if node.ID == c.id {
					selfTarget = true
					break
				}
			}
			if selfTarget && !c.disk.valid("blocks", id) {
				for _, peer := range peers {
					data, getErr := c.get(ctx, peer.Endpoint, "blocks", id, token, cloudMaxBlobSize)
					if getErr == nil {
						if c.disk.put("blocks", id, bytes.NewReader(data), cloudMaxBlobSize) == nil {
							budget--
						}
						break
					}
				}
			}
			if !c.disk.valid("blocks", id) {
				continue
			}
			file, err := c.disk.get("blocks", id)
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(file, cloudMaxBlobSize+1))
			file.Close()
			if err != nil || cloudHash(data) != id {
				continue
			}
			for _, node := range ranked[:targetCount] {
				if budget <= 0 {
					return
				}
				if node.ID == c.id || c.hasRemote(ctx, node, id, token) {
					continue
				}
				if c.put(ctx, node.Endpoint, "blocks", id, token, data) == nil {
					budget--
				}
			}
		}
	}
}
