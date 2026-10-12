package main

import (
	"database/sql"
	"net/http"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type tagSuggestion struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
	Score  int    `json:"-"`
}

type suggestionFile struct {
	ID   string
	Path string
	Name string
}

var (
	filenameParts = regexp.MustCompile(`\p{Han}+|[A-Za-z][A-Za-z0-9]*|[0-9]+`)
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	folderCode    = regexp.MustCompile(`^\d+[a-z]$`)
	episodeSuffix = regexp.MustCompile(`(?i)(?:s\d+(?:e\d*)?|season\s*\d+|ep(?:isode)?\s*\d*)$`)
	releaseGroup  = regexp.MustCompile(`^\[[^\]]+\]`)
	commonWords   = map[string]struct{}{
		"a": {}, "an": {}, "and": {}, "backup": {}, "copy": {}, "draft": {}, "file": {}, "final": {},
		"aeidr": {}, "asapid": {}, "ascsallc": {}, "auxdata": {}, "bdjo": {}, "bdmt": {}, "bdmv": {},
		"bluray": {}, "certificate": {}, "clipinf": {}, "codec": {}, "content": {}, "deu": {}, "dl": {},
		"duplicate": {}, "eng": {}, "ep": {}, "episode": {}, "fra": {}, "info": {},
		"display": {}, "ita": {}, "jar": {}, "jpn": {}, "kor": {}, "metadata": {}, "movieobject": {}, "new": {},
		"nld": {}, "old": {}, "playlist": {}, "player": {}, "por": {}, "region": {}, "rus": {}, "season": {},
		"spa": {}, "stream": {}, "the": {}, "tmp": {}, "temp": {}, "untitled": {}, "version": {}, "vidity": {},
		"webdl": {}, "webrip": {}, "x264": {}, "x265": {}, "zho": {},
		"副本": {}, "临时": {}, "备份": {}, "新建": {}, "最终": {}, "文件": {}, "未命名": {}, "副本文件": {},
	}
	commonFolders = map[string]struct{}{
		"assets": {}, "auxdata": {}, "backup": {}, "backups": {}, "bdjo": {}, "bdmv": {}, "cache": {},
		"certificate": {}, "clipinf": {}, "desktop": {}, "documents": {}, "downloads": {}, "duplicate": {}, "dfex": {},
		"images": {}, "music": {}, "node_modules": {}, "pictures": {}, "program files": {}, "public": {}, "source": {},
		"playlist": {}, "src": {}, "stream": {}, "stopcards": {}, "temp": {}, "tmp": {}, "videos": {},
		"windows": {}, "jar": {}, "movieobject": {}, "下载": {}, "桌面": {}, "文档": {}, "图片": {},
		"视频": {}, "音乐": {},
	}
)

func (s *server) tagSuggestions(w http.ResponseWriter, r *http.Request) {
	var resourcePath, resourceName, rootID, rootPath string
	err := s.db.QueryRow(`SELECT r.path,r.name,r.root_id,roots.path FROM resources r JOIN roots ON roots.id=r.root_id WHERE r.id=?`, r.PathValue("id")).Scan(&resourcePath, &resourceName, &rootID, &rootPath)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	currentTags := map[string]struct{}{}
	tagRows, err := s.db.Query(`SELECT t.name FROM tags t JOIN resource_tags rt ON rt.tag_id=t.id WHERE rt.resource_id=?`, r.PathValue("id"))
	if err == nil {
		for tagRows.Next() {
			var name string
			if tagRows.Scan(&name) == nil {
				currentTags[normalizeTag(name)] = struct{}{}
			}
		}
		tagRows.Close()
	}

	parent := filepath.Dir(resourcePath)
	separator := string(filepath.Separator)
	prefix := parent
	if !strings.HasSuffix(prefix, separator) {
		prefix += separator
	}
	prefixLength := utf8.RuneCountInString(prefix)
	rows, err := s.db.Query(`SELECT id,path,name FROM resources WHERE root_id=? AND substr(path,1,?)=? ORDER BY path LIMIT 1001`, rootID, prefixLength, prefix)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	siblings := make([]suggestionFile, 0, 64)
	for rows.Next() {
		var item suggestionFile
		if rows.Scan(&item.ID, &item.Path, &item.Name) == nil && sameFolder(filepath.Dir(item.Path), parent) {
			siblings = append(siblings, item)
		}
	}
	rows.Close()
	if len(siblings) == 0 {
		siblings = append(siblings, suggestionFile{ID: r.PathValue("id"), Path: resourcePath, Name: resourceName})
	}

	suggestions := map[string]tagSuggestion{}
	add := func(name, reason string, score int) {
		name = strings.TrimSpace(name)
		key := normalizeTag(name)
		if key == "" || len([]rune(name)) > 40 || !meaningfulTag(name) {
			return
		}
		if _, exists := currentTags[key]; exists {
			return
		}
		if existing, exists := suggestions[key]; !exists || score > existing.Score {
			suggestions[key] = tagSuggestion{Name: name, Reason: reason, Score: score}
		}
	}

	if relative, relErr := filepath.Rel(rootPath, parent); relErr == nil && relative != "." {
		parts := strings.Split(relative, separator)
		for i := len(parts) - 1; i >= 0; i-- {
			if !meaningfulFolder(parts[i]) {
				continue
			}
			reason := "所在文件夹"
			score := 95 - (len(parts)-1-i)*8
			if i < len(parts)-1 {
				reason = "上级文件夹"
			}
			add(parts[i], reason, score)
		}
	}

	stems := make([]string, 0, len(siblings))
	termCounts := map[string]int{}
	for _, sibling := range siblings {
		stem := filenameStem(sibling.Name)
		stems = append(stems, stem)
		seen := map[string]struct{}{}
		for _, term := range filenameParts.FindAllString(camelBoundary.ReplaceAllString(stem, "$1 $2"), -1) {
			key := normalizeTag(term)
			if key == "" || !meaningfulTag(term) {
				continue
			}
			if _, ok := seen[key]; !ok {
				termCounts[key]++
				seen[key] = struct{}{}
			}
		}
	}
	for term, count := range termCounts {
		if count >= 2 {
			add(term, "同目录文件共有词 · "+itoa(count)+" 项", 82+min(count, 10))
		}
	}
	for _, term := range filenameParts.FindAllString(camelBoundary.ReplaceAllString(filenameStem(resourceName), "$1 $2"), -1) {
		add(term, "来自文件名", 52)
	}
	stem := filenameStem(resourceName)
	if utf8.RuneCountInString(stem) <= 24 {
		add(stem, "完整文件名", 42)
	}
	if shared := commonLeadingPrefix(stems); shared != "" {
		add(shared, "同目录文件共同前缀", 88)
	}

	ids := make([]any, 0, len(siblings))
	for _, sibling := range siblings {
		ids = append(ids, sibling.ID)
	}
	if len(ids) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
		query := `SELECT t.name,COUNT(DISTINCT rt.resource_id) FROM resource_tags rt JOIN tags t ON t.id=rt.tag_id WHERE rt.resource_id IN (` + placeholders + `) GROUP BY t.id HAVING COUNT(DISTINCT rt.resource_id)>=2 ORDER BY COUNT(DISTINCT rt.resource_id) DESC LIMIT 8`
		rows, queryErr := s.db.Query(query, ids...)
		if queryErr == nil {
			for rows.Next() {
				var name string
				var count int
				if rows.Scan(&name, &count) == nil {
					add(name, "同目录已有 "+itoa(count)+" 项使用", 110+min(count, 10))
				}
			}
			rows.Close()
		}
	}

	result := make([]tagSuggestion, 0, len(suggestions))
	for _, suggestion := range suggestions {
		result = append(result, suggestion)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Score == result[j].Score {
			return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
		}
		return result[i].Score > result[j].Score
	})
	if len(result) > 8 {
		result = result[:8]
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": result})
}

func sameFolder(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func filenameStem(name string) string {
	return strings.TrimSpace(strings.TrimSuffix(name, filepath.Ext(name)))
}

func normalizeTag(name string) string {
	return strings.ToLower(strings.Trim(name, " \t\r\n-_.,;:()[]{}"))
}

func meaningfulTag(name string) bool {
	name = normalizeTag(name)
	runes := []rune(name)
	if len(runes) < 2 || len(runes) > 40 {
		return false
	}
	allDigits := true
	for _, char := range runes {
		if !unicode.IsDigit(char) {
			allDigits = false
			break
		}
	}
	if allDigits {
		return false
	}
	if _, common := commonWords[name]; common {
		return false
	}
	return true
}

func meaningfulFilenameTerm(name string) bool {
	if !meaningfulTag(name) {
		return false
	}
	runes := []rune(normalizeTag(name))
	latin := true
	for _, char := range runes {
		if char >= 128 {
			latin = false
			break
		}
		if unicode.IsDigit(char) {
			return false
		}
	}
	return !latin || len(runes) >= 4
}

func meaningfulFolder(name string) bool {
	name = normalizeTag(name)
	if _, common := commonFolders[name]; common {
		return false
	}
	// Short drive or quality markers such as "3b" rarely make useful tags.
	if folderCode.MatchString(name) {
		return false
	}
	return meaningfulTag(name)
}

func commonLeadingPrefix(values []string) string {
	if len(values) < 2 {
		return ""
	}
	prefix := []rune(values[0])
	for _, value := range values[1:] {
		current := []rune(value)
		limit := min(len(prefix), len(current))
		i := 0
		for i < limit && unicode.ToLower(prefix[i]) == unicode.ToLower(current[i]) {
			i++
		}
		prefix = prefix[:i]
		if len(prefix) == 0 {
			return ""
		}
	}
	cleaned := strings.TrimSpace(episodeSuffix.ReplaceAllString(string(prefix), ""))
	cleaned = releaseGroup.ReplaceAllString(cleaned, "")
	for len([]rune(cleaned)) > 0 {
		runes := []rune(cleaned)
		last := runes[len(runes)-1]
		if unicode.IsLetter(last) || unicode.IsNumber(last) {
			break
		}
		cleaned = string(runes[:len(runes)-1])
	}
	prefix = []rune(cleaned)
	for len(prefix) > 0 && unicode.IsDigit(prefix[len(prefix)-1]) {
		prefix = prefix[:len(prefix)-1]
	}
	return strings.TrimSpace(string(prefix))
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
