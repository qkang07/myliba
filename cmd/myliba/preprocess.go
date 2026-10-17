package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	thumbnailOperation   = "thumbnail"
	fingerprintOperation = "fingerprint"
)

func preprocessOperation(kind string) string {
	if kind == "image" || kind == "video" {
		return thumbnailOperation
	}
	return fingerprintOperation
}

type preprocessJob struct {
	ResourceID string `json:"resourceId"`
	Name       string `json:"name"`
	Operation  string `json:"operation"`
	Status     string `json:"status"`
	Priority   int    `json:"priority"`
	UpdatedAt  string `json:"updatedAt"`
	Error      string `json:"error"`
}

func preprocessVersion(info os.FileInfo) string {
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func (s *server) wakePreprocessing() {
	if s.jobsWake != nil {
		select {
		case s.jobsWake <- struct{}{}:
		default:
		}
	}
}

func (s *server) enqueuePreprocessing(id, kind string, info os.FileInfo) error {
	operation := preprocessOperation(kind)
	status := "pending"
	if operation == thumbnailOperation {
		cachePath, _ := s.thumbnailCachePath(id, info, thumbnailWidth, 0)
		if _, err := os.Stat(cachePath); err == nil {
			status = "done"
		}
	} else {
		var contentID, modified string
		var size int64
		err := s.db.QueryRow(`SELECT COALESCE(v.content_id,''),COALESCE(v.size,0),COALESCE(v.modified_at,'') FROM resources r
            LEFT JOIN versions v ON v.id=r.current_version_id WHERE r.id=?`, id).Scan(&contentID, &size, &modified)
		if err != nil {
			return err
		}
		if contentID != "" && size == info.Size() && modified == info.ModTime().UTC().Format(time.RFC3339Nano) {
			status = "done"
		}
	}
	if _, err := s.db.Exec(`DELETE FROM preprocess_jobs WHERE resource_id=? AND operation<>?`, id, operation); err != nil {
		return err
	}
	stamp := now()
	_, err := s.db.Exec(`INSERT INTO preprocess_jobs(resource_id,operation,version_key,status,created_at,updated_at)
        VALUES(?,?,?,?,?,?)
        ON CONFLICT(resource_id,operation) DO UPDATE SET
          version_key=excluded.version_key,status=excluded.status,priority=0,priority_at=0,
          updated_at=excluded.updated_at,error=''
        WHERE preprocess_jobs.version_key<>excluded.version_key
           OR (preprocess_jobs.status='done' AND excluded.status='pending')`,
		id, operation, preprocessVersion(info), status, stamp, stamp)
	if status == "pending" && err == nil {
		s.wakePreprocessing()
	}
	return err
}

func (s *server) startPreprocessing() error {
	if s.jobsStop == nil {
		s.jobsStop = make(chan struct{})
	}
	if _, err := s.db.Exec(`UPDATE preprocess_jobs SET status='pending' WHERE status='running'`); err != nil {
		return err
	}
	go s.preprocessWorker()
	go s.backfillPreprocessing()
	s.wakePreprocessing()
	return nil
}

func (s *server) backfillPreprocessing() {
	rows, err := s.db.Query(`SELECT id,path,kind FROM resources WHERE available=1`)
	if err != nil {
		log.Printf("preprocess backfill: %v", err)
		return
	}
	type item struct{ id, path, kind string }
	var items []item
	for rows.Next() {
		var entry item
		if err := rows.Scan(&entry.id, &entry.path, &entry.kind); err == nil {
			items = append(items, entry)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("preprocess backfill: %v", err)
	}
	rows.Close()
	for _, entry := range items {
		info, err := os.Stat(entry.path)
		if err == nil && info.Mode().IsRegular() {
			if err := s.enqueuePreprocessing(entry.id, entry.kind, info); err != nil {
				log.Printf("preprocess backfill %s: %v", entry.id, err)
			}
		}
	}
}

func (s *server) claimPreprocessJob() (string, string, string, error) {
	var id, operation, version string
	err := s.db.QueryRow(`SELECT resource_id,operation,version_key FROM preprocess_jobs
        WHERE status='pending' ORDER BY priority DESC,priority_at DESC,created_at ASC LIMIT 1`).Scan(&id, &operation, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	_, err = s.db.Exec(`UPDATE preprocess_jobs SET status='running',updated_at=?
        WHERE resource_id=? AND operation=? AND status='pending'`, now(), id, operation)
	return id, operation, version, err
}

func (s *server) preprocessWorker() {
	for {
		select {
		case <-s.jobsStop:
			return
		default:
		}
		id, operation, version, err := s.claimPreprocessJob()
		if err != nil {
			log.Printf("preprocess queue: %v", err)
		}
		if id == "" {
			select {
			case <-s.jobsWake:
			case <-s.jobsStop:
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		var path, kind string
		err = s.db.QueryRow(`SELECT path,kind FROM resources WHERE id=? AND available=1`, id).Scan(&path, &kind)
		if err == nil {
			var info os.FileInfo
			info, err = os.Stat(path)
			if err == nil && !info.Mode().IsRegular() {
				err = fmt.Errorf("source is not a regular file")
			}
			if err == nil && preprocessVersion(info) != version {
				err = s.enqueuePreprocessing(id, kind, info)
				if err == nil {
					continue
				}
			}
			if err == nil {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				if operation == thumbnailOperation {
					err = s.ensureThumbnail(ctx, id, path, kind, info, thumbnailWidth, thumbnailHeight, 0)
				} else {
					err = s.fingerprintResource(ctx, id, path, info)
				}
				cancel()
			}
		}
		status, message := "done", ""
		if err != nil {
			status, message = "failed", err.Error()
			if len(message) > 300 {
				message = message[:300]
			}
			log.Printf("preprocess %s %s: %v", operation, id, err)
		}
		if _, updateErr := s.db.Exec(`UPDATE preprocess_jobs SET status=?,updated_at=?,error=? WHERE resource_id=? AND operation=? AND version_key=?`,
			status, now(), message, id, operation, version); updateErr != nil {
			log.Printf("preprocess queue update: %v", updateErr)
		}
	}
}

func (s *server) fingerprintResource(ctx context.Context, id, path string, info os.FileInfo) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	buffer := make([]byte, 512*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if preprocessVersion(after) != preprocessVersion(info) {
		return fmt.Errorf("source changed during fingerprinting")
	}
	result, err := s.db.Exec(`UPDATE versions SET content_id=? WHERE id=(
        SELECT current_version_id FROM resources WHERE id=?) AND size=? AND modified_at=?`,
		hex.EncodeToString(hash.Sum(nil)), id, info.Size(), info.ModTime().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if updated, _ := result.RowsAffected(); updated == 0 {
		return fmt.Errorf("resource version changed")
	}
	return nil
}

func (s *server) listPreprocessJobs(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{"pending": 0, "running": 0, "failed": 0, "done": 0}
	rows, err := s.db.Query(`SELECT status,COUNT(*) FROM preprocess_jobs GROUP BY status`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取任务队列")
		return
	}
	for rows.Next() {
		var status string
		var count int
		if rows.Scan(&status, &count) == nil {
			counts[status] = count
		}
	}
	rows.Close()
	rows, err = s.db.Query(`SELECT j.resource_id,r.name,j.operation,j.status,j.priority,j.updated_at,j.error
        FROM preprocess_jobs j JOIN resources r ON r.id=j.resource_id
        ORDER BY CASE j.status WHEN 'running' THEN 0 WHEN 'pending' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,
                 j.priority DESC,j.priority_at DESC,j.updated_at DESC LIMIT 100`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法读取任务队列")
		return
	}
	jobs := []preprocessJob{}
	for rows.Next() {
		var job preprocessJob
		if rows.Scan(&job.ResourceID, &job.Name, &job.Operation, &job.Status, &job.Priority, &job.UpdatedAt, &job.Error) == nil {
			jobs = append(jobs, job)
		}
	}
	rows.Close()
	visible := map[string]string{}
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	if len(ids) > 200 {
		writeError(w, http.StatusBadRequest, "资源过多")
		return
	}
	for _, id := range ids {
		if id == "" || len(id) > 128 {
			continue
		}
		var status string
		if s.db.QueryRow(`SELECT status FROM preprocess_jobs WHERE resource_id=? AND operation=?`, id, thumbnailOperation).Scan(&status) == nil {
			visible[id] = status
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "jobs": jobs, "visible": visible})
}

func (s *server) prioritizePreprocessJobs(w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil || len(input.IDs) > 200 {
		writeError(w, http.StatusBadRequest, "资源列表无效")
		return
	}
	base := time.Now().UnixNano()
	for index, id := range input.IDs {
		if id == "" || len(id) > 128 {
			continue
		}
		var path, kind string
		if err := s.db.QueryRow(`SELECT path,kind FROM resources WHERE id=? AND available=1`, id).Scan(&path, &kind); err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := s.enqueuePreprocessing(id, kind, info); err != nil {
			writeError(w, http.StatusInternalServerError, "无法安排预处理任务")
			return
		}
		if _, err := s.db.Exec(`UPDATE preprocess_jobs SET priority=1,priority_at=? WHERE resource_id=? AND operation=? AND status='pending'`,
			base+int64(len(input.IDs)-index), id, preprocessOperation(kind)); err != nil {
			writeError(w, http.StatusInternalServerError, "无法调整任务优先级")
			return
		}
	}
	s.wakePreprocessing()
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) retryPreprocessJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var path, kind string
	if err := s.db.QueryRow(`SELECT path,kind FROM resources WHERE id=? AND available=1`, id).Scan(&path, &kind); err != nil {
		writeError(w, http.StatusNotFound, "资源不存在或不可用")
		return
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeError(w, http.StatusGone, "源文件不可用")
		return
	}
	if err := s.enqueuePreprocessing(id, kind, info); err != nil {
		writeError(w, http.StatusInternalServerError, "无法安排任务")
		return
	}
	result, err := s.db.Exec(`UPDATE preprocess_jobs SET status='pending',priority=1,priority_at=?,updated_at=?,error=''
        WHERE resource_id=? AND operation=? AND status IN ('failed','done')`, time.Now().UnixNano(), now(), id, preprocessOperation(kind))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "无法重试任务")
		return
	}
	if changed, _ := result.RowsAffected(); changed > 0 {
		s.wakePreprocessing()
	}
	w.WriteHeader(http.StatusAccepted)
}
