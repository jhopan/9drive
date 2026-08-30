package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateAndListVirtualFolders(t *testing.T) {
	app := newTestApp(t)
	token, _ := registerAndLogin(t, app, "folders@example.test")
	r := app.Router()

	request := httptest.NewRequest(http.MethodPost, "/folders", bytes.NewBufferString(`{"name":"Dokumen"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		Folder struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"folder"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Folder.Name != "Dokumen" || created.Folder.ID == "" {
		t.Fatalf("folder = %#v", created.Folder)
	}

	request = httptest.NewRequest(http.MethodGet, "/folders", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", w.Code, w.Body.String())
	}
	var listed struct {
		Folders []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"folders"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Folders) != 1 || listed.Folders[0].ID != created.Folder.ID {
		t.Fatalf("folders = %#v", listed.Folders)
	}
}

func TestFoldersAreUserScoped(t *testing.T) {
	app := newTestApp(t)
	tokenA, _ := registerAndLogin(t, app, "folder-a@example.test")
	tokenB, _ := registerAndLogin(t, app, "folder-b@example.test")
	r := app.Router()
	request := httptest.NewRequest(http.MethodPost, "/folders", bytes.NewBufferString(`{"name":"Private"}`))
	request.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	request = httptest.NewRequest(http.MethodGet, "/folders", nil)
	request.Header.Set("Authorization", "Bearer "+tokenB)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	var listed struct {
		Folders []any `json:"folders"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listed)
	if len(listed.Folders) != 0 {
		t.Fatalf("other user sees %#v", listed.Folders)
	}
}
