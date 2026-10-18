package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
)

type hubProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	HasToken bool   `json:"hasToken"`
}

type hubProfilesView struct {
	Profiles []hubProfile `json:"profiles"`
	ActiveID string       `json:"activeId"`
	Status   string       `json:"status"`
}

type hubProfiles struct {
	db         *sql.DB
	iceServers []iceServerConfig
	mu         sync.Mutex
	activeID   string
	status     string
	cancel     context.CancelFunc
	generation uint64
}

func newHubProfiles(db *sql.DB, iceServers []iceServerConfig) (*hubProfiles, error) {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS hub_profiles (
        id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, token TEXT NOT NULL
    );
    CREATE TABLE IF NOT EXISTS hub_settings (
        key TEXT PRIMARY KEY, value TEXT NOT NULL
    );`); err != nil {
		return nil, err
	}
	profiles := &hubProfiles{db: db, iceServers: iceServers, status: "disabled"}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hub_profiles`).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		hubURL := strings.TrimSpace(os.Getenv("MYLIBA_HUB_URL"))
		token := strings.TrimSpace(os.Getenv("MYLIBA_NODE_TOKEN"))
		if hubURL != "" {
			if err := validateHubURL(hubURL); err != nil {
				return nil, err
			}
			if len(token) < 32 {
				return nil, errors.New("MYLIBA_NODE_TOKEN must be at least 32 characters")
			}
			id := newID()
			if _, err := db.Exec(`INSERT INTO hub_profiles(id,name,url,token) VALUES(?,?,?,?)`, id, "原有服务端", hubURL, token); err != nil {
				return nil, err
			}
			if _, err := db.Exec(`INSERT INTO hub_settings(key,value) VALUES('active_hub',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, id); err != nil {
				return nil, err
			}
		}
	}
	var active string
	err := db.QueryRow(`SELECT value FROM hub_settings WHERE key='active_hub'`).Scan(&active)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if active != "" {
		if err := profiles.activate(active); err != nil {
			// A removed or invalid saved profile must not prevent local library startup.
			profiles.activeID = ""
			profiles.status = "disabled"
		}
	}
	return profiles, nil
}

func (h *hubProfiles) setStatus(generation uint64, status string) {
	h.mu.Lock()
	if h.generation == generation {
		h.status = status
	}
	h.mu.Unlock()
}

func (h *hubProfiles) activate(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var hubURL, token string
	if id != "" {
		if err := h.db.QueryRow(`SELECT url,token FROM hub_profiles WHERE id=?`, id).Scan(&hubURL, &token); err != nil {
			return err
		}
		if err := validateHubURL(hubURL); err != nil {
			return err
		}
		if len(token) < 32 {
			return errors.New("节点密钥至少需要 32 个字符")
		}
	}
	if _, err := h.db.Exec(`INSERT INTO hub_settings(key,value) VALUES('active_hub',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, id); err != nil {
		return err
	}
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.generation++
	h.activeID = id
	h.status = "disabled"
	if id != "" {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel = cancel
		h.status = "connecting"
		generation := h.generation
		go runNodeAgent(ctx, hubURL, token, h.iceServers, func(status string) { h.setStatus(generation, status) })
	}
	return nil
}

func (h *hubProfiles) view() (hubProfilesView, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rows, err := h.db.Query(`SELECT id,name,url FROM hub_profiles ORDER BY rowid`)
	if err != nil {
		return hubProfilesView{}, err
	}
	defer rows.Close()
	view := hubProfilesView{Profiles: []hubProfile{}, ActiveID: h.activeID, Status: h.status}
	for rows.Next() {
		var p hubProfile
		if err := rows.Scan(&p.ID, &p.Name, &p.URL); err != nil {
			return hubProfilesView{}, err
		}
		p.HasToken = true
		view.Profiles = append(view.Profiles, p)
	}
	return view, rows.Err()
}

func localHubMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Host != "127.0.0.1:4317" || r.Header.Get("Origin") != "http://127.0.0.1:4317" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusForbidden, "仅允许本机页面修改服务端配置")
		return false
	}
	return true
}

func (h *hubProfiles) list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Host != "127.0.0.1:4317" {
		writeError(w, http.StatusForbidden, "仅允许本机访问")
		return
	}
	view, err := h.view()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "读取服务端配置失败")
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *hubProfiles) save(w http.ResponseWriter, r *http.Request) {
	if !localHubMutation(w, r) {
		return
	}
	var input struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "服务端配置格式错误")
		return
	}
	input.ID, input.Name, input.URL, input.Token = strings.TrimSpace(input.ID), strings.TrimSpace(input.Name), strings.TrimSpace(input.URL), strings.TrimSpace(input.Token)
	if input.Name == "" || len([]rune(input.Name)) > 80 {
		writeError(w, http.StatusBadRequest, "服务端名称需为 1 到 80 个字符")
		return
	}
	if len(input.URL) > 2048 || validateHubURL(input.URL) != nil {
		writeError(w, http.StatusBadRequest, "请输入有效的 wss:// 服务端地址")
		return
	}
	if len(input.Token) > 4096 {
		writeError(w, http.StatusBadRequest, "节点密钥过长")
		return
	}
	h.mu.Lock()
	id := input.ID
	if id == "" {
		id = newID()
	}
	if input.ID != "" && input.Token == "" {
		err := h.db.QueryRow(`SELECT token FROM hub_profiles WHERE id=?`, id).Scan(&input.Token)
		if err != nil {
			h.mu.Unlock()
			writeError(w, http.StatusNotFound, "服务端配置不存在")
			return
		}
	}
	if len(input.Token) < 32 {
		h.mu.Unlock()
		writeError(w, http.StatusBadRequest, "节点密钥至少需要 32 个字符")
		return
	}
	var result sql.Result
	var err error
	if input.ID == "" {
		result, err = h.db.Exec(`INSERT INTO hub_profiles(id,name,url,token) VALUES(?,?,?,?)`, id, input.Name, input.URL, input.Token)
	} else {
		result, err = h.db.Exec(`UPDATE hub_profiles SET name=?,url=?,token=? WHERE id=?`, input.Name, input.URL, input.Token, id)
	}
	if err == nil && result != nil && input.ID != "" {
		var affected int64
		affected, err = result.RowsAffected()
		if err == nil && affected == 0 {
			err = sql.ErrNoRows
		}
	}
	active := h.activeID == id
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存服务端配置失败")
		return
	}
	if active || input.ID == "" {
		if err := h.activate(id); err != nil {
			writeError(w, http.StatusInternalServerError, "连接服务端失败")
			return
		}
	}
	h.list(w, r)
}

func (h *hubProfiles) selectActive(w http.ResponseWriter, r *http.Request) {
	if !localHubMutation(w, r) {
		return
	}
	var input struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "请选择服务端")
		return
	}
	if err := h.activate(strings.TrimSpace(input.ID)); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "服务端配置不存在")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	h.list(w, r)
}

func (h *hubProfiles) remove(w http.ResponseWriter, r *http.Request) {
	if !localHubMutation(w, r) {
		return
	}
	id := r.PathValue("id")
	h.mu.Lock()
	active := h.activeID == id
	h.mu.Unlock()
	if active {
		if err := h.activate(""); err != nil {
			writeError(w, http.StatusInternalServerError, "断开服务端失败")
			return
		}
	}
	h.mu.Lock()
	result, err := h.db.Exec(`DELETE FROM hub_profiles WHERE id=?`, id)
	h.mu.Unlock()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "删除服务端配置失败")
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeError(w, http.StatusNotFound, "服务端配置不存在")
		return
	}
	h.list(w, r)
}
