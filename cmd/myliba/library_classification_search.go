package main

import (
	"database/sql"
	"errors"
	"net/url"
	"sort"
	"strings"
)

type libraryIndexCondition struct {
	Mode string `json:"mode"`
	ID   string `json:"id"`
}

type libraryClassificationSearch struct {
	Browse          *libraryIndexCondition
	Filters         []libraryIndexCondition
	NeedsOrganizing string
	FacetParentID   string
	FacetRequested  bool
}

type libraryIndexFacet struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Mode  string `json:"mode"`
	Count int    `json:"count"`
}

func validClassificationSearchID(id string) bool {
	return id != "" && len(id) <= 200 && !strings.ContainsRune(id, 0)
}

func parseClassificationSearch(values url.Values) (libraryClassificationSearch, error) {
	result := libraryClassificationSearch{NeedsOrganizing: strings.TrimSpace(values.Get("needsOrganizing")), FacetRequested: values.Has("facetParentId"), FacetParentID: strings.TrimSpace(values.Get("facetParentId"))}
	if result.NeedsOrganizing != "" && result.NeedsOrganizing != "true" && result.NeedsOrganizing != "false" {
		return result, errors.New("待整理筛选无效")
	}
	browseID := strings.TrimSpace(values.Get("browseId"))
	browseMode := strings.TrimSpace(values.Get("browseMode"))
	if browseID != "" {
		if !validClassificationSearchID(browseID) {
			return result, errors.New("浏览索引无效")
		}
		if browseMode == "" {
			browseMode = "subtree"
		}
		if browseMode != "direct" && browseMode != "subtree" {
			return result, errors.New("浏览范围无效")
		}
		result.Browse = &libraryIndexCondition{Mode: browseMode, ID: browseID}
	} else if browseMode != "" {
		return result, errors.New("浏览范围缺少索引")
	}
	seen := map[string]bool{}
	for _, raw := range values["indexFilter"] {
		mode, id, ok := strings.Cut(strings.TrimSpace(raw), ":")
		id = strings.TrimSpace(id)
		if !ok || mode != "direct" && mode != "subtree" || !validClassificationSearchID(id) {
			return result, errors.New("索引筛选无效")
		}
		key := mode + "\x00" + id
		if !seen[key] {
			seen[key] = true
			result.Filters = append(result.Filters, libraryIndexCondition{Mode: mode, ID: id})
		}
	}
	if len(result.Filters) > 20 {
		return result, errors.New("索引筛选最多 20 项")
	}
	sort.Slice(result.Filters, func(i, j int) bool {
		if result.Filters[i].ID == result.Filters[j].ID {
			return result.Filters[i].Mode < result.Filters[j].Mode
		}
		return result.Filters[i].ID < result.Filters[j].ID
	})
	if result.FacetRequested && result.FacetParentID != "" && !validClassificationSearchID(result.FacetParentID) {
		return result, errors.New("facet 上级索引无效")
	}
	return result, nil
}

func (filter libraryClassificationSearch) cursorKey() string {
	parts := []string{filter.NeedsOrganizing}
	if filter.Browse != nil {
		parts = append(parts, "browse", filter.Browse.Mode, filter.Browse.ID)
	}
	for _, condition := range filter.Filters {
		parts = append(parts, "filter", condition.Mode, condition.ID)
	}
	if filter.FacetRequested {
		parts = append(parts, "facet", filter.FacetParentID)
	}
	return strings.Join(parts, "\x00")
}

func validateClassificationSearch(db *sql.DB, libraryID string, filter libraryClassificationSearch) error {
	ids := []string{}
	if filter.Browse != nil {
		ids = append(ids, filter.Browse.ID)
	}
	for _, condition := range filter.Filters {
		ids = append(ids, condition.ID)
	}
	if filter.FacetRequested && filter.FacetParentID != "" {
		ids = append(ids, filter.FacetParentID)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM classification_nodes WHERE library_id=? AND index_id=? AND deleted=0`, libraryID, id).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return sql.ErrNoRows
		}
	}
	return nil
}

func classificationConditionSQL(fileAlias, libraryID string, condition libraryIndexCondition) (string, []any) {
	if condition.Mode == "direct" {
		return `EXISTS (SELECT 1 FROM classification_memberships cm WHERE cm.library_id=? AND cm.file_id=` + fileAlias + `.file_id AND cm.index_id=?)`, []any{libraryID, condition.ID}
	}
	return `EXISTS (SELECT 1 FROM classification_memberships cm JOIN classification_ancestors ca ON ca.library_id=cm.library_id AND ca.descendant_id=cm.index_id WHERE cm.library_id=? AND cm.file_id=` + fileAlias + `.file_id AND ca.ancestor_id=?)`, []any{libraryID, condition.ID}
}

func classificationSearchSQL(fileAlias, libraryID string, filter libraryClassificationSearch, includeBrowse bool) ([]string, []any) {
	where := []string{}
	args := []any{}
	if includeBrowse && filter.Browse != nil {
		clause, values := classificationConditionSQL(fileAlias, libraryID, *filter.Browse)
		where, args = append(where, clause), append(args, values...)
	}
	for _, condition := range filter.Filters {
		clause, values := classificationConditionSQL(fileAlias, libraryID, condition)
		where, args = append(where, clause), append(args, values...)
	}
	if filter.NeedsOrganizing == "true" {
		where = append(where, `NOT EXISTS (SELECT 1 FROM classification_memberships cm WHERE cm.library_id=? AND cm.file_id=`+fileAlias+`.file_id AND cm.has_manual=1)`)
		args = append(args, libraryID)
	} else if filter.NeedsOrganizing == "false" {
		where = append(where, `EXISTS (SELECT 1 FROM classification_memberships cm WHERE cm.library_id=? AND cm.file_id=`+fileAlias+`.file_id AND cm.has_manual=1)`)
		args = append(args, libraryID)
	}
	return where, args
}

func classificationKeywordSQL(fileAlias, libraryID, pattern string) (string, []any) {
	return `EXISTS (SELECT 1 FROM classification_memberships cm JOIN classification_ancestors ca ON ca.library_id=cm.library_id AND ca.descendant_id=cm.index_id JOIN classification_nodes cn ON cn.library_id=ca.library_id AND cn.index_id=ca.ancestor_id WHERE cm.library_id=? AND cm.file_id=` + fileAlias + `.file_id AND cn.deleted=0 AND cn.name_key LIKE ? ESCAPE '\')`, []any{libraryID, pattern}
}

func classificationSearchGeneration(db *sql.DB, libraryID string) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return classificationSnapshotGeneration(tx, libraryID)
}

type classificationSearchMatcher struct {
	browse       map[string]bool
	filters      []map[string]bool
	manual       map[string]bool
	needs        string
	keyword      map[string]bool
	facetNodes   []libraryIndexFacet
	facetMatches []map[string]bool
}

func classificationFileSet(db *sql.DB, libraryID string, condition libraryIndexCondition) (map[string]bool, error) {
	result := map[string]bool{}
	var rows *sql.Rows
	var err error
	if condition.Mode == "direct" {
		rows, err = db.Query(`SELECT file_id FROM classification_memberships WHERE library_id=? AND index_id=?`, libraryID, condition.ID)
	} else {
		rows, err = db.Query(`SELECT DISTINCT cm.file_id FROM classification_memberships cm JOIN classification_ancestors ca ON ca.library_id=cm.library_id AND ca.descendant_id=cm.index_id WHERE cm.library_id=? AND ca.ancestor_id=?`, libraryID, condition.ID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func loadClassificationSearchMatcher(db *sql.DB, libraryID, keyword string, filter libraryClassificationSearch) (classificationSearchMatcher, error) {
	matcher := classificationSearchMatcher{manual: map[string]bool{}, needs: filter.NeedsOrganizing, keyword: map[string]bool{}}
	var err error
	if filter.Browse != nil {
		matcher.browse, err = classificationFileSet(db, libraryID, *filter.Browse)
		if err != nil {
			return matcher, err
		}
	}
	for _, condition := range filter.Filters {
		set, setErr := classificationFileSet(db, libraryID, condition)
		if setErr != nil {
			return matcher, setErr
		}
		matcher.filters = append(matcher.filters, set)
	}
	if filter.NeedsOrganizing != "" {
		rows, queryErr := db.Query(`SELECT DISTINCT file_id FROM classification_memberships WHERE library_id=? AND has_manual=1`, libraryID)
		if queryErr != nil {
			return matcher, queryErr
		}
		for rows.Next() {
			var id string
			if queryErr = rows.Scan(&id); queryErr != nil {
				rows.Close()
				return matcher, queryErr
			}
			matcher.manual[id] = true
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return matcher, queryErr
		}
		rows.Close()
	}
	if keyword != "" {
		pattern := "%" + strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(foldedTagKey(keyword)) + "%"
		rows, queryErr := db.Query(`SELECT DISTINCT cm.file_id FROM classification_memberships cm JOIN classification_ancestors ca ON ca.library_id=cm.library_id AND ca.descendant_id=cm.index_id JOIN classification_nodes cn ON cn.library_id=ca.library_id AND cn.index_id=ca.ancestor_id WHERE cm.library_id=? AND cn.deleted=0 AND cn.name_key LIKE ? ESCAPE '\'`, libraryID, pattern)
		if queryErr != nil {
			return matcher, queryErr
		}
		for rows.Next() {
			var id string
			if queryErr = rows.Scan(&id); queryErr != nil {
				rows.Close()
				return matcher, queryErr
			}
			matcher.keyword[id] = true
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return matcher, queryErr
		}
		rows.Close()
	}
	if filter.FacetRequested {
		var rows *sql.Rows
		if filter.FacetParentID == "" {
			rows, err = db.Query(`SELECT index_id,name FROM classification_nodes WHERE library_id=? AND parent_id IS NULL AND deleted=0 ORDER BY name_key,index_id`, libraryID)
		} else {
			rows, err = db.Query(`SELECT index_id,name FROM classification_nodes WHERE library_id=? AND parent_id=? AND deleted=0 ORDER BY name_key,index_id`, libraryID, filter.FacetParentID)
		}
		if err != nil {
			return matcher, err
		}
		for rows.Next() {
			var facet libraryIndexFacet
			if err = rows.Scan(&facet.ID, &facet.Name); err != nil {
				rows.Close()
				return matcher, err
			}
			facet.Mode = "subtree"
			matcher.facetNodes = append(matcher.facetNodes, facet)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return matcher, err
		}
		rows.Close()
		for _, facet := range matcher.facetNodes {
			set, setErr := classificationFileSet(db, libraryID, libraryIndexCondition{Mode: "subtree", ID: facet.ID})
			if setErr != nil {
				return matcher, setErr
			}
			matcher.facetMatches = append(matcher.facetMatches, set)
		}
	}
	return matcher, nil
}

func (matcher classificationSearchMatcher) matches(fileID string, includeBrowse bool) bool {
	if includeBrowse && matcher.browse != nil && !matcher.browse[fileID] {
		return false
	}
	for _, set := range matcher.filters {
		if !set[fileID] {
			return false
		}
	}
	if matcher.needs == "true" && matcher.manual[fileID] || matcher.needs == "false" && !matcher.manual[fileID] {
		return false
	}
	return true
}
