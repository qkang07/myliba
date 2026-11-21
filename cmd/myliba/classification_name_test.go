package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClassificationNameNormalizationVectors(t *testing.T) {
	vectors := []struct {
		input, name, key string
	}{
		{"  Cafe\u0301  ", "Café", "café"},
		{"\u212Bngstro\u0308m", "Ångström", "ångström"},
		{"Straße", "Straße", "strasse"},
		{"Σίσυφος", "Σίσυφος", "σίσυφοσ"},
		{"\u212Aelvin", "Kelvin", "kelvin"},
		{"İ", "İ", "i\u0307"},
		{"\u00a0项目\u3000", "项目", "项目"},
	}
	for _, vector := range vectors {
		name, valid := validClassificationName(vector.input)
		if !valid || name != vector.name || classificationNameKey(vector.input) != vector.key {
			t.Fatalf("name vector %q: valid=%v name=%q key=%q", vector.input, valid, name, classificationNameKey(vector.input))
		}
	}
}

func TestClassificationSiblingNamesUseCanonicalUnicode(t *testing.T) {
	app, client := classificationTestNode(t, "node-names")
	if err := initializeClassificationLibrary(app.db, client.libraryID()); err != nil {
		t.Fatal(err)
	}
	create := func(requestID, name string) *httptest.ResponseRecorder {
		body, err := json.Marshal(map[string]string{"requestId": requestID, "name": name})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		app.createClassificationIndex(response, httptest.NewRequest(http.MethodPost, "/api/library/indexes", strings.NewReader(string(body))))
		return response
	}
	created := create("unicode-1", "Cafe\u0301")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"name":"Café"`) {
		t.Fatalf("canonical create: %d %s", created.Code, created.Body.String())
	}
	duplicate := create("unicode-2", "CAFÉ")
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "indexNameConflict") {
		t.Fatalf("canonical duplicate accepted: %d %s", duplicate.Code, duplicate.Body.String())
	}
}
