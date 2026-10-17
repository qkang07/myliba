package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
)

const mebibyte int64 = 1024 * 1024

type attributeTag struct {
	Key      string `json:"key"`
	Category string `json:"category"`
	Label    string `json:"label"`
	Count    int    `json:"count,omitempty"`
}

var kindLabels = map[string]string{
	"image": "图片", "video": "视频", "audio": "音频",
	"document": "文档", "file": "其他文件",
}
var sizeLabels = map[string]string{
	"lt1m": "小于 1 MB", "1to10m": "1–10 MB",
	"10to100m": "10–100 MB", "gte100m": "100 MB 以上",
}
var kindOrder = []string{"image", "video", "audio", "document", "file"}
var sizeOrder = []string{"lt1m", "1to10m", "10to100m", "gte100m"}

func resourceSizeBucket(size int64) string {
	switch {
	case size < mebibyte:
		return "lt1m"
	case size < 10*mebibyte:
		return "1to10m"
	case size < 100*mebibyte:
		return "10to100m"
	default:
		return "gte100m"
	}
}

func resourceExtension(name string) string {
	dot := strings.LastIndexByte(name, '.')
	if dot <= 0 || dot == len(name)-1 {
		return ""
	}
	extension := strings.ToLower(name[dot+1:])
	if !validAttributeExtension(extension) {
		return ""
	}
	return extension
}

func validAttributeExtension(extension string) bool {
	if len(extension) == 0 || len(extension) > 16 {
		return false
	}
	for _, character := range extension {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func resourceAttributeTags(name, kind string, size int64) []attributeTag {
	tags := []attributeTag{}
	if label, ok := kindLabels[kind]; ok {
		tags = append(tags, attributeTag{Key: "type:" + kind, Category: "type", Label: label})
	}
	if extension := resourceExtension(name); extension != "" {
		tags = append(tags, attributeTag{Key: "extension:" + extension, Category: "extension", Label: strings.ToUpper(extension)})
	}
	bucket := resourceSizeBucket(size)
	tags = append(tags, attributeTag{Key: "size:" + bucket, Category: "size", Label: sizeLabels[bucket]})
	return tags
}

func normalizeAttributeKeys(raw []string) ([]string, error) {
	if len(raw) > 30 {
		return nil, fmt.Errorf("属性标签过多")
	}
	seen := map[string]bool{}
	keys := []string{}
	for _, item := range raw {
		key := strings.ToLower(strings.TrimSpace(item))
		category, value, ok := strings.Cut(key, ":")
		if !ok {
			return nil, fmt.Errorf("属性标签无效")
		}
		switch category {
		case "type":
			_, ok = kindLabels[value]
		case "size":
			_, ok = sizeLabels[value]
		case "extension":
			ok = validAttributeExtension(value)
		default:
			ok = false
		}
		if !ok {
			return nil, fmt.Errorf("属性标签无效")
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func attributeFilterSQL(keys []string) ([]string, []any) {
	grouped := map[string][]string{}
	for _, key := range keys {
		category, value, _ := strings.Cut(key, ":")
		grouped[category] = append(grouped[category], value)
	}
	clauses := []string{}
	args := []any{}
	if values := grouped["type"]; len(values) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
		clauses = append(clauses, "r.kind IN ("+placeholders+")")
		for _, value := range values {
			args = append(args, value)
		}
	}
	if values := grouped["extension"]; len(values) > 0 {
		parts := []string{}
		for _, value := range values {
			parts = append(parts, "r.name LIKE ?")
			args = append(args, "%."+value)
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	if values := grouped["size"]; len(values) > 0 {
		parts := []string{}
		for _, value := range values {
			switch value {
			case "lt1m":
				parts = append(parts, "r.size < ?")
				args = append(args, mebibyte)
			case "1to10m":
				parts = append(parts, "(r.size >= ? AND r.size < ?)")
				args = append(args, mebibyte, 10*mebibyte)
			case "10to100m":
				parts = append(parts, "(r.size >= ? AND r.size < ?)")
				args = append(args, 10*mebibyte, 100*mebibyte)
			case "gte100m":
				parts = append(parts, "r.size >= ?")
				args = append(args, 100*mebibyte)
			}
		}
		clauses = append(clauses, "("+strings.Join(parts, " OR ")+")")
	}
	return clauses, args
}

func (s *server) attributeTags(w http.ResponseWriter, r *http.Request) {
	selectedKeys, err := normalizeAttributeKeys(r.URL.Query()["attribute"])
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	counts := map[string]attributeTag{}
	for _, category := range []string{"type", "size", "extension"} {
		where, args, err := resourceWhereWithoutAttributeCategory(r, category)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		rows, err := s.db.Query("SELECT r.name,r.kind,r.size FROM resources r WHERE "+strings.Join(where, " AND "), args...)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "无法读取属性标签")
			return
		}
		for rows.Next() {
			var name, kind string
			var size int64
			if err := rows.Scan(&name, &kind, &size); err != nil {
				rows.Close()
				writeError(w, http.StatusInternalServerError, "无法读取属性标签")
				return
			}
			for _, tag := range resourceAttributeTags(name, kind, size) {
				if tag.Category != category {
					continue
				}
				tag.Count = counts[tag.Key].Count + 1
				counts[tag.Key] = tag
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			writeError(w, http.StatusInternalServerError, "无法读取属性标签")
			return
		}
		rows.Close()
	}
	for _, key := range selectedKeys {
		if _, exists := counts[key]; exists {
			continue
		}
		category, value, _ := strings.Cut(key, ":")
		label := ""
		switch category {
		case "type":
			label = kindLabels[value]
		case "size":
			label = sizeLabels[value]
		case "extension":
			label = strings.ToUpper(value)
		}
		counts[key] = attributeTag{Key: key, Category: category, Label: label}
	}
	items := []attributeTag{}
	for _, kind := range kindOrder {
		if item, ok := counts["type:"+kind]; ok {
			items = append(items, item)
		}
	}
	for _, size := range sizeOrder {
		if item, ok := counts["size:"+size]; ok {
			items = append(items, item)
		}
	}
	extensions := []string{}
	for key := range counts {
		if strings.HasPrefix(key, "extension:") {
			extensions = append(extensions, key)
		}
	}
	sort.Strings(extensions)
	for _, key := range extensions {
		items = append(items, counts[key])
	}
	writeJSON(w, http.StatusOK, items)
}
