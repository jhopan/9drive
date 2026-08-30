package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilesListReturnsOnlyCurrentUserMetadata(t *testing.T) {
	app := newTestApp(t)
	tokenA, userA := registerAndLogin(t, app, "files-a@example.test")
	_, userB := registerAndLogin(t, app, "files-b@example.test")
	for _, item := range []struct{ id, userID, accountID, providerID, name string }{
		{"account-a", userA.ID, "account-a", "drive-a", ""},
		{"account-b", userB.ID, "account-b", "drive-b", ""},
	} {
		_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,scopes) VALUES (?,?,?,?,?,?)`, item.id, item.userID, "google_drive", item.providerID, item.providerID+"@example.test", "[]")
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := app.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,provider,provider_file_id,name,mime_type,size_bytes) VALUES (?,?,?,?,?,?,?,?)`, "file-a", userA.ID, "account-a", "google_drive", "google-file-a", "A.pdf", "application/pdf", 123)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,provider,provider_file_id,name,mime_type,size_bytes) VALUES (?,?,?,?,?,?,?,?)`, "file-b", userB.ID, "account-b", "google_drive", "google-file-b", "B.pdf", "application/pdf", 456)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/files", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Files []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			SizeBytes string `json:"sizeBytes"`
		} `json:"files"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Files) != 1 || body.Files[0].ID != "file-a" || body.Files[0].SizeBytes != "123" {
		t.Fatalf("files = %#v", body.Files)
	}
}

func TestFilesListFiltersByFolder(t *testing.T) {
	app := newTestApp(t)
	token, user := registerAndLogin(t, app, "files-folder@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,scopes) VALUES (?,?,?,?,?,?)`, "account", user.ID, "google_drive", "drive", "drive@example.test", "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO folders (id,user_id,name,color) VALUES (?,?,?,?)`, "folder", user.ID, "Docs", "text-blue-500")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,folder_id,provider,provider_file_id,name,mime_type,size_bytes) VALUES (?,?,?,?,?,?,?,?,?)`, "inside", user.ID, "account", "folder", "google_drive", "in", "inside.txt", "text/plain", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,provider,provider_file_id,name,mime_type,size_bytes) VALUES (?,?,?,?,?,?,?,?)`, "root", user.ID, "account", "google_drive", "root", "root.txt", "text/plain", 1)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/files?folderId=folder", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	var body struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Files) != 1 || body.Files[0].ID != "inside" {
		t.Fatalf("files = %#v", body.Files)
	}
}
