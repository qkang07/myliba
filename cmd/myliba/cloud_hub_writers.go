package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type catalogWriterState struct {
	MinFormat  int    `json:"minFormat"`
	UpgradedAt string `json:"upgradedAt,omitempty"`
	Epoch      int64  `json:"epoch"`
	Retired    bool   `json:"retired,omitempty"`
}

var (
	errWriterRetired       = errors.New("catalog writer has been retired")
	errWriterEpochStale    = errors.New("catalog writer epoch is superseded")
	errWriterFormatBlocked = errors.New("catalog writer format is below the registered minimum")
)

type catalogWriterRegistry struct {
	path    string
	writers map[string]catalogWriterState
}

func newCatalogWriterRegistry(dir string) (*catalogWriterRegistry, error) {
	r := &catalogWriterRegistry{path: filepath.Join(dir, "catalog-writers.json"), writers: map[string]catalogWriterState{}}
	data, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var saved map[string]catalogWriterState
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, errors.New("invalid Hub catalog writer registry")
	}
	for key, state := range saved {
		parts := strings.SplitN(key, "\x00", 2)
		if len(parts) != 2 || !validCloudHash(parts[0]) || parts[1] == "" || len(parts[1]) > 128 {
			return nil, errors.New("invalid Hub catalog writer key")
		}
		if state.MinFormat < 3 || state.MinFormat > 4 || state.Epoch < 0 {
			return nil, errors.New("invalid Hub catalog writer state")
		}
		r.writers[key] = state
	}
	return r, nil
}

func (r *catalogWriterRegistry) saveLocked() error {
	keys := make([]string, 0, len(r.writers))
	for key := range r.writers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]catalogWriterState, len(keys))
	for _, key := range keys {
		ordered[key] = r.writers[key]
	}
	data, err := json.Marshal(ordered)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(r.path), ".catalog-writers-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), r.path)
}

// admit checks the incoming record against the persistent writer gate. It must
// be called under the Hub's pruneMu so concurrent writes are serialized.
func (r *catalogWriterRegistry) admit(record cloudRecord) error {
	key := record.LibraryID + "\x00" + record.AuthorID
	state := r.writers[key]
	if state.Retired {
		return errWriterRetired
	}
	if state.Epoch > 0 && record.Format >= 4 && record.AuthorEpoch < state.Epoch {
		return errWriterEpochStale
	}
	if record.Format < state.MinFormat {
		if state.UpgradedAt != "" && record.Format == 3 {
			incomingAt, err := time.Parse(time.RFC3339Nano, record.CreatedAt)
			upgradedAt, parseErr := time.Parse(time.RFC3339Nano, state.UpgradedAt)
			if err == nil && parseErr == nil && incomingAt.Before(upgradedAt) {
				return nil
			}
		}
		return errWriterFormatBlocked
	}
	return nil
}

// recordAccepted updates the registry after a successful catalog write.
func (r *catalogWriterRegistry) recordAccepted(record cloudRecord) bool {
	key := record.LibraryID + "\x00" + record.AuthorID
	state := r.writers[key]
	changed := false
	if record.Format > state.MinFormat {
		state.MinFormat = record.Format
		changed = true
	}
	if record.Format >= 4 && state.UpgradedAt == "" {
		state.UpgradedAt = record.CreatedAt
		changed = true
	} else if record.Format >= 4 && state.UpgradedAt != "" {
		incomingAt, err := time.Parse(time.RFC3339Nano, record.CreatedAt)
		upgradedAt, parseErr := time.Parse(time.RFC3339Nano, state.UpgradedAt)
		if err == nil && parseErr == nil && incomingAt.Before(upgradedAt) {
			state.UpgradedAt = record.CreatedAt
			changed = true
		}
	}
	if record.AuthorEpoch > state.Epoch {
		state.Epoch = record.AuthorEpoch
		changed = true
	}
	if changed {
		r.writers[key] = state
	}
	return changed
}

func (r *catalogWriterRegistry) retire(libraryID, authorID string, epoch int64) bool {
	key := libraryID + "\x00" + authorID
	state := r.writers[key]
	if state.Retired {
		return false
	}
	state.Retired = true
	if epoch > state.Epoch {
		state.Epoch = epoch
	}
	r.writers[key] = state
	return true
}

func (r *catalogWriterRegistry) rejoin(libraryID, authorID string, epoch int64) bool {
	key := libraryID + "\x00" + authorID
	state := r.writers[key]
	if !state.Retired || epoch <= state.Epoch {
		return false
	}
	state.Retired = false
	state.Epoch = epoch
	r.writers[key] = state
	return true
}

func (r *catalogWriterRegistry) writerState(libraryID, authorID string) (catalogWriterState, bool) {
	state, ok := r.writers[libraryID+"\x00"+authorID]
	return state, ok
}

// rebuildFromCatalog reconstructs writer state from existing catalog records.
// Used on first startup when no registry file exists yet.
func (r *catalogWriterRegistry) rebuildFromCatalog(disk *cloudDisk) error {
	ids, err := disk.list("catalog")
	if err != nil {
		return err
	}
	for _, id := range ids {
		file, openErr := disk.get("catalog", id)
		if openErr != nil {
			continue
		}
		var record cloudRecord
		decodeErr := json.NewDecoder(file).Decode(&record)
		file.Close()
		if decodeErr != nil || record.Format < 3 {
			continue
		}
		r.recordAccepted(record)
	}
	return r.saveLocked()
}

func (c *cloudHub) writerRoutes(mux *http.ServeMux, authorize func(http.Handler) http.Handler) {
	mux.Handle("POST /cloud/writers/retire", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			LibraryID string `json:"libraryId"`
			AuthorID  string `json:"authorId"`
			Epoch     int64  `json:"epoch"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil ||
			!validCloudHash(input.LibraryID) || input.AuthorID == "" || len(input.AuthorID) > 128 || input.Epoch < 1 {
			http.Error(w, "invalid retire request", 400)
			return
		}
		c.pruneMu.Lock()
		defer c.pruneMu.Unlock()
		if !c.writers.retire(input.LibraryID, input.AuthorID, input.Epoch) {
			writeJSON(w, 200, map[string]any{"retired": true, "alreadyRetired": true})
			return
		}
		if err := c.writers.saveLocked(); err != nil {
			c.writers.writers[input.LibraryID+"\x00"+input.AuthorID] = catalogWriterState{}
			http.Error(w, "cannot persist writer retirement", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"retired": true})
	})))
	mux.Handle("POST /cloud/writers/rejoin", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			LibraryID string `json:"libraryId"`
			AuthorID  string `json:"authorId"`
			Epoch     int64  `json:"epoch"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&input) != nil ||
			!validCloudHash(input.LibraryID) || input.AuthorID == "" || len(input.AuthorID) > 128 || input.Epoch < 1 {
			http.Error(w, "invalid rejoin request", 400)
			return
		}
		c.pruneMu.Lock()
		defer c.pruneMu.Unlock()
		if !c.writers.rejoin(input.LibraryID, input.AuthorID, input.Epoch) {
			http.Error(w, "writer is not retired or epoch is not higher", 409)
			return
		}
		if err := c.writers.saveLocked(); err != nil {
			state := c.writers.writers[input.LibraryID+"\x00"+input.AuthorID]
			state.Retired = true
			c.writers.writers[input.LibraryID+"\x00"+input.AuthorID] = state
			http.Error(w, "cannot persist writer rejoin", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"rejoined": true, "epoch": input.Epoch})
	})))
	mux.Handle("GET /cloud/writers/state", authorize(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		libraryID, authorID := r.URL.Query().Get("libraryId"), r.URL.Query().Get("authorId")
		if !validCloudHash(libraryID) || authorID == "" || len(authorID) > 128 {
			http.Error(w, "invalid writer identity", 400)
			return
		}
		c.pruneMu.Lock()
		state, exists := c.writers.writerState(libraryID, authorID)
		c.pruneMu.Unlock()
		if !exists {
			writeJSON(w, 200, map[string]any{"registered": false, "minFormat": 3, "epoch": int64(0), "retired": false})
			return
		}
		writeJSON(w, 200, map[string]any{"registered": true, "minFormat": state.MinFormat, "epoch": state.Epoch, "retired": state.Retired, "upgradedAt": state.UpgradedAt})
	})))
}