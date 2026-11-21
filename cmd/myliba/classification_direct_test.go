package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewLibraryStartsActiveWithoutLegacyImport(t *testing.T) {
	app, client := classificationTestNode(t, "node-new")
	libraryID := client.libraryID()
	if err := initializeClassificationLibrary(app.db, libraryID); err != nil {
		t.Fatal(err)
	}
	if classificationMode(app.db, libraryID) != "active" {
		t.Fatal("new library did not start in active classification mode")
	}
	if client.catalogWriterFormat() != 5 {
		t.Fatalf("new library writer format: %d", client.catalogWriterFormat())
	}

	// Development-era folder/tag rows are deliberately outside the new model.
	seedClassificationLibrary(t, app.db, "legacy-folder", "Legacy")
	report, err := refreshClassificationProjection(app.db, libraryID, "node-new")
	if err != nil {
		t.Fatal(err)
	}
	if report.NodeCount != 0 || report.MembershipCount != 0 {
		t.Fatalf("legacy classification imported into new library: %+v", report)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/library/indexes", strings.NewReader(`{"requestId":"req-1","name":"Projects"}`))
	response := httptest.NewRecorder()
	app.createClassificationIndex(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create index: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Node classificationIndexView `json:"node"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Node.ID == "" || created.Node.Name != "Projects" {
		t.Fatalf("created index: %+v", created)
	}

	if err := initializeClassificationLibrary(app.db, libraryID); err != nil {
		t.Fatal(err)
	}
	report, err = refreshClassificationProjection(app.db, libraryID, "node-new")
	if err != nil {
		t.Fatal(err)
	}
	if report.NodeCount != 1 {
		t.Fatalf("new-model data lost after idempotent initialization: %+v", report)
	}
	status := httptest.NewRecorder()
	app.classificationStatus(status, httptest.NewRequest(http.MethodGet, "/api/library/classification-status", nil))
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"mode":"active"`) || !strings.Contains(status.Body.String(), `"catalogFormat":5`) {
		t.Fatalf("classification status: %d %s", status.Code, status.Body.String())
	}

	legacyWrite := httptest.NewRecorder()
	app.createFolder(legacyWrite, httptest.NewRequest(http.MethodPost, "/api/folders", strings.NewReader(`{"name":"Old entry"}`)))
	if legacyWrite.Code != http.StatusConflict || !strings.Contains(legacyWrite.Body.String(), "classificationUpgradeRequired") {
		t.Fatalf("legacy write was not rejected: %d %s", legacyWrite.Code, legacyWrite.Body.String())
	}
	if allowedRemoteRequest(http.MethodGet, "/api/library/legacy-folders") || allowedRemoteRequest(http.MethodPost, "/api/library/legacy-folders/merge") {
		t.Fatal("legacy folder identity routes remain exposed through the remote proxy")
	}
}

func classificationTestNode(t *testing.T, nodeID string) (*server, *cloudClient) {
	t.Helper()
	db := classificationTestDB(t)
	runtime := testCloudRuntime(t, t.TempDir(), "")
	runtime.id = nodeID
	runtime.db = db
	client, err := newCloudClient(runtime)
	if err != nil {
		t.Fatal(err)
	}
	return &server{db: db, cloud: client}, client
}
