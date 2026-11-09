package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type cloudBlockCleanupPlan struct {
	PlanID            string               `json:"planId"`
	CatalogGeneration string               `json:"catalogGeneration"`
	Eligible          []libraryCleanupItem `json:"eligible"`
	Blocked           []string             `json:"blocked"`
}

// Storage nodes have no decryption keys. They only delete old blocks which
// are absent from every Hub-committed catalog record, across all libraries.
func (c *cloudRuntime) makeBlockCleanupPlan(ctx context.Context) (cloudBlockCleanupPlan, error) {
	plan := cloudBlockCleanupPlan{Eligible: []libraryCleanupItem{}, Blocked: []string{}}
	ws, token, err := c.credentials()
	if err != nil || ws == "" || len(token) < 32 {
		plan.Blocked = append(plan.Blocked, "Hub 不可达或未配置")
		return plan, nil
	}
	hub, err := hubHTTPURL(ws)
	if err != nil {
		plan.Blocked = append(plan.Blocked, "Hub 地址无效")
		return plan, nil
	}
	if err = c.syncCatalog(ctx, hub, token); err != nil {
		plan.Blocked = append(plan.Blocked, "无法同步 Hub 目录")
		return plan, nil
	}
	remote, err := c.remoteIDs(ctx, hub, token)
	if err != nil {
		plan.Blocked = append(plan.Blocked, "无法读取 Hub 目录")
		return plan, nil
	}
	local, err := c.disk.list("catalog")
	if err != nil {
		return plan, err
	}
	if strings.Join(remote, "\x00") != strings.Join(local, "\x00") {
		plan.Blocked = append(plan.Blocked, "Hub 与本机目录不一致")
		return plan, nil
	}
	plan.CatalogGeneration = cloudHash([]byte(strings.Join(remote, "\x00")))
	referenced := map[string]bool{}
	for _, id := range remote {
		file, openErr := c.disk.get("catalog", id)
		if openErr != nil {
			return plan, openErr
		}
		var record cloudRecord
		decodeErr := json.NewDecoder(file).Decode(&record)
		file.Close()
		if decodeErr != nil {
			return plan, decodeErr
		}
		for _, block := range record.BlockIDs {
			if !validCloudHash(block) {
				return plan, errors.New("invalid catalog block")
			}
			referenced[block] = true
		}
	}
	blocks, err := c.disk.list("blocks")
	if err != nil {
		return plan, err
	}
	for _, id := range blocks {
		if referenced[id] || len(plan.Eligible) >= 1000 {
			continue
		}
		modified, size, statErr := c.disk.objectStat("blocks", id)
		if statErr == nil && time.Since(modified) >= libraryCleanupRetention {
			plan.Eligible = append(plan.Eligible, libraryCleanupItem{Kind: "block", ID: id, Bytes: size})
		}
	}
	raw, _ := json.Marshal(struct {
		Catalog string
		Items   []libraryCleanupItem
	}{plan.CatalogGeneration, plan.Eligible})
	plan.PlanID = cloudHash(raw)
	return plan, nil
}

func (c *cloudRuntime) cleanupRoutes(mux *http.ServeMux, authorize func(http.Handler) http.Handler) {
	mux.Handle("GET /cloud/cleanup/plan", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		plan, err := c.makeBlockCleanupPlan(ctx)
		if err != nil {
			http.Error(w, "cleanup plan unavailable", 500)
			return
		}
		writeJSON(w, 200, plan)
	})))
	mux.Handle("POST /cloud/cleanup/execute", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			PlanID       string `json:"planId"`
			CleanupToken string `json:"cleanupToken"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil || !validCloudHash(input.PlanID) || !validCloudHash(input.CleanupToken) {
			http.Error(w, "invalid cleanup plan", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		plan, err := c.makeBlockCleanupPlan(ctx)
		if err != nil {
			http.Error(w, "cleanup plan unavailable", 500)
			return
		}
		if len(plan.Blocked) != 0 || plan.PlanID != input.PlanID {
			writeJSON(w, 409, map[string]any{"error": "目录或清理计划已变化", "plan": plan})
			return
		}
		ws, token, err := c.credentials()
		if err != nil || ws == "" || len(token) < 32 {
			http.Error(w, "Hub credentials unavailable", 503)
			return
		}
		hub, err := hubHTTPURL(ws)
		if err != nil {
			http.Error(w, "invalid Hub URL", 503)
			return
		}
		// The query node's member gate is proven to the Hub, which issues a
		// lease and blocks catalog writes for the deletion window. Re-verify
		// before every block so an expired or superseded lease fails closed.
		verify := func() bool {
			payload, _ := json.Marshal(map[string]string{"token": input.CleanupToken, "generation": plan.CatalogGeneration})
			response, requestErr := c.request(ctx, http.MethodPost, hub, "/cloud/cleanup/verify", token, bytes.NewReader(payload))
			if requestErr != nil {
				return false
			}
			defer response.Body.Close()
			return response.StatusCode == http.StatusOK
		}
		if !verify() {
			writeJSON(w, 409, map[string]any{"error": "清理授权无效或已过期"})
			return
		}
		removed := []string{}
		for _, item := range plan.Eligible {
			if !verify() {
				writeJSON(w, 409, map[string]any{"error": "Hub 目录已变化或清理授权已过期", "removedBlocks": removed})
				return
			}
			modified, size, statErr := c.disk.objectStat("blocks", item.ID)
			if statErr != nil || size != item.Bytes || time.Since(modified) < libraryCleanupRetention {
				continue
			}
			if err := c.disk.remove("blocks", item.ID); err != nil {
				writeJSON(w, 500, map[string]any{"error": "块删除失败", "removedBlocks": removed})
				return
			}
			removed = append(removed, item.ID)
		}
		writeJSON(w, 200, map[string]any{"removedBlocks": removed, "sourceOriginalsRemoved": 0})
	})))
}
