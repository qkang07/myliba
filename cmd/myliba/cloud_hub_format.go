package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type libraryFormatState struct {
	MinFormat   int    `json:"minFormat"`
	ActivatedAt string `json:"activatedAt"`
	Corrupt     bool   `json:"-"`
}

func validLibraryFormatState(state libraryFormatState) bool {
	if state.MinFormat != 5 {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, state.ActivatedAt)
	return err == nil
}

func parseLibraryFormatStates(data []byte) (map[string]libraryFormatState, error) {
	var saved map[string]libraryFormatState
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, errors.New("invalid Hub library format registry")
	}
	states := make(map[string]libraryFormatState, len(saved))
	for library, state := range saved {
		if !validCloudHash(library) {
			return nil, errors.New("invalid Hub library format identity")
		}
		if !validLibraryFormatState(state) {
			state = libraryFormatState{Corrupt: true}
		}
		states[library] = state
	}
	return states, nil
}

func (c *cloudHub) loadLibraryFormats() {
	data, err := os.ReadFile(c.libraryFormatPath)
	if os.IsNotExist(err) {
		return
	}
	if err == nil {
		if states, parseErr := parseLibraryFormatStates(data); parseErr == nil {
			c.libraryFormats = states
			return
		}
	}
	if backup, backupErr := os.ReadFile(c.libraryFormatPath + ".bak"); backupErr == nil {
		if states, parseErr := parseLibraryFormatStates(backup); parseErr == nil {
			c.libraryFormats = states
			log.Printf("Hub library format registry recovered from backup")
			if saveErr := c.saveLibraryFormatsLocked(); saveErr != nil {
				log.Printf("Hub library format registry re-save failed: %v", saveErr)
			}
			return
		}
	}
	c.libraryFormatsPaused = true
	log.Printf("Hub library format registry unavailable; new catalog writes are paused")
}

func (c *cloudHub) saveLibraryFormatsLocked() error {
	libraries := make([]string, 0, len(c.libraryFormats))
	for library, state := range c.libraryFormats {
		if state.Corrupt {
			return errors.New("cannot persist corrupt library format state")
		}
		libraries = append(libraries, library)
	}
	sort.Strings(libraries)
	ordered := make(map[string]libraryFormatState, len(libraries))
	for _, library := range libraries {
		ordered[library] = c.libraryFormats[library]
	}
	data, err := json.Marshal(ordered)
	if err != nil {
		return err
	}
	if current, readErr := os.ReadFile(c.libraryFormatPath); readErr == nil {
		if writeErr := os.WriteFile(c.libraryFormatPath+".bak", current, 0600); writeErr != nil {
			return writeErr
		}
	}
	file, err := os.CreateTemp(filepath.Dir(c.libraryFormatPath), ".library-formats-*")
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
	return os.Rename(file.Name(), c.libraryFormatPath)
}

// libraryFormatGateLocked establishes format 5 on the first new-format write
// and rejects every later older-format write for the same library.
func (c *cloudHub) libraryFormatGateLocked(record cloudRecord, id string) (status int, message string, confirmed bool) {
	if c.libraryFormatsPaused {
		return http.StatusLocked, "library format state unavailable", false
	}
	state, exists := c.libraryFormats[record.LibraryID]
	if !exists {
		if record.Format >= 5 {
			state = libraryFormatState{MinFormat: 5, ActivatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			c.libraryFormats[record.LibraryID] = state
			if err := c.saveLibraryFormatsLocked(); err != nil {
				delete(c.libraryFormats, record.LibraryID)
				return http.StatusInternalServerError, "cannot persist library format boundary", false
			}
		}
		return 0, "", false
	}
	if state.Corrupt {
		return http.StatusLocked, "library format state unavailable", false
	}
	if c.disk.valid("catalog", id) {
		return 0, "", true
	}
	if record.Format < state.MinFormat {
		return http.StatusConflict, "classificationFormatRequired", false
	}
	return 0, "", false
}
