package main

import (
	"net/http"
	"path/filepath"
	"sort"
	"strings"
)

type batchTagSuggestion struct {
	Name     string   `json:"name"`
	Reason   string   `json:"reason"`
	Count    int      `json:"count"`
	Examples []string `json:"examples"`
	IDs      []string `json:"ids"`
	Score    int      `json:"-"`
}

type batchSuggestionResource struct {
	ID   string
	Root string
	Path string
	Name string
	Tags map[string]struct{}
}

type batchSuggestionAccumulator struct {
	Suggestion batchTagSuggestion
	IDs        map[string]struct{}
	Examples   map[string]struct{}
}

func (s *server) tagBatchSuggestions(w http.ResponseWriter, _ *http.Request) {
	rows, err := s.db.Query(`SELECT r.id,r.root_id,r.path,r.name,t.name
		FROM resources r
		LEFT JOIN resource_tags rt ON rt.resource_id=r.id
		LEFT JOIN tags t ON t.id=rt.tag_id
		ORDER BY r.root_id,r.path`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resources := make([]*batchSuggestionResource, 0, 256)
	byID := map[string]*batchSuggestionResource{}
	for rows.Next() {
		var id, rootID, path, name string
		var tagName *string
		if err := rows.Scan(&id, &rootID, &path, &name, &tagName); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		item := byID[id]
		if item == nil {
			item = &batchSuggestionResource{ID: id, Root: rootID, Path: path, Name: name, Tags: map[string]struct{}{}}
			byID[id] = item
			resources = append(resources, item)
		}
		if tagName != nil {
			item.Tags[normalizeTag(*tagName)] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := rows.Close(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	accumulated := map[string]*batchSuggestionAccumulator{}
	add := func(name, reason string, baseScore int, group []*batchSuggestionResource) {
		name = strings.TrimSpace(name)
		key := normalizeTag(name)
		if key == "" || len([]rune(name)) > 40 || !meaningfulTag(name) {
			return
		}
		missing := make([]*batchSuggestionResource, 0, len(group))
		for _, item := range group {
			if _, exists := item.Tags[key]; !exists {
				missing = append(missing, item)
			}
		}
		if len(missing) < 3 {
			return
		}
		score := baseScore + min(len(missing), 20)
		candidate := accumulated[key]
		if candidate == nil {
			candidate = &batchSuggestionAccumulator{
				Suggestion: batchTagSuggestion{Name: name, Reason: reason},
				IDs:        map[string]struct{}{},
				Examples:   map[string]struct{}{},
			}
			accumulated[key] = candidate
		}
		if score > candidate.Suggestion.Score {
			candidate.Suggestion.Reason = reason
			candidate.Suggestion.Score = score
		}
		for _, item := range missing {
			candidate.IDs[item.ID] = struct{}{}
			if len(candidate.Examples) < 3 {
				candidate.Examples[item.Name] = struct{}{}
			}
		}
	}

	byFolder := map[string][]*batchSuggestionResource{}
	for _, item := range resources {
		folder := filepath.Dir(item.Path)
		folderKey := item.Root + "\x00" + strings.ToLower(filepath.Clean(folder))
		byFolder[folderKey] = append(byFolder[folderKey], item)
	}
	folderKeys := make([]string, 0, len(byFolder))
	for key := range byFolder {
		folderKeys = append(folderKeys, key)
	}
	sort.Strings(folderKeys)
	for _, key := range folderKeys {
		group := byFolder[key]
		if len(group) < 3 {
			continue
		}
		folderName := filepath.Base(filepath.Dir(group[0].Path))
		if meaningfulFolder(folderName) {
			add(folderName, "同一文件夹中的文件", 90, group)
		}
		stems := make([]string, 0, len(group))
		for _, item := range group {
			stems = append(stems, filenameStem(item.Name))
		}
		if prefix := commonLeadingPrefix(stems); prefix != "" {
			add(prefix, "同目录文件名的共同前缀", 110, group)
		}
	}

	termGroups := map[string][]*batchSuggestionResource{}
	for _, item := range resources {
		seen := map[string]struct{}{}
		stem := filenameStem(item.Name)
		for _, term := range filenameParts.FindAllString(camelBoundary.ReplaceAllString(stem, "$1 $2"), -1) {
			key := normalizeTag(term)
			if _, exists := seen[key]; exists || !meaningfulFilenameTerm(term) {
				continue
			}
			seen[key] = struct{}{}
			termGroups[key] = append(termGroups[key], item)
		}
	}
	terms := make([]string, 0, len(termGroups))
	for term := range termGroups {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	for _, term := range terms {
		group := termGroups[term]
		if len(group) >= 4 {
			add(term, "多个文件名包含此关键词", 75, group)
		}
	}

	result := make([]batchTagSuggestion, 0, len(accumulated))
	for _, candidate := range accumulated {
		candidate.Suggestion.IDs = make([]string, 0, len(candidate.IDs))
		for id := range candidate.IDs {
			candidate.Suggestion.IDs = append(candidate.Suggestion.IDs, id)
		}
		sort.Strings(candidate.Suggestion.IDs)
		candidate.Suggestion.Count = len(candidate.Suggestion.IDs)
		candidate.Suggestion.Examples = make([]string, 0, len(candidate.Examples))
		for name := range candidate.Examples {
			candidate.Suggestion.Examples = append(candidate.Suggestion.Examples, name)
		}
		sort.Strings(candidate.Suggestion.Examples)
		result = append(result, candidate.Suggestion)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	if len(result) > 20 {
		result = result[:20]
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": result, "scanned": len(resources)})
}
