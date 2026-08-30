package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResumableChunkCompletesAndCreatesFile(t *testing.T) {
	app := newTestApp(t)
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/session" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Content-Range") != "bytes 0-2/3" {
			t.Errorf("range = %q", r.Header.Get("Content-Range"))
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access" {
			t.Errorf("auth = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "google-file", "name": "test.txt", "mimeType": "text/plain", "size": "3"})
	}))
	defer google.Close()
	token, user := registerAndLogin(t, app, "chunk@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,access_token_encrypted,scopes) VALUES (?,?,?,?,?,?,?)`, "account", user.ID, "google_drive", "google", "drive@example.test", app.encrypt("access"), "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO upload_sessions (id,user_id,target_connected_account_id,file_name,mime_type,size_bytes,status,google_session_uri) VALUES (?,?,?,?,?,?,?,?)`, "session", user.ID, "account", "test.txt", "text/plain", 3, "uploading", app.encrypt(google.URL+"/session"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/uploads/resumable/chunk/session", bytes.NewBufferString("abc"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Range", "bytes 0-2/3")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if response.Status != "completed" {
		t.Fatalf("response = %s", w.Body.String())
	}
	var count int
	if err := app.DB.QueryRow(`SELECT COUNT(*) FROM files WHERE provider_file_id='google-file'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("file count = %d err=%v", count, err)
	}
}

func TestResumableStatusReturnsOffset(t *testing.T) {
	app := newTestApp(t)
	token, user := registerAndLogin(t, app, "status@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,access_token_encrypted,scopes) VALUES (?,?,?,?,?,?,?)`, "account", user.ID, "google_drive", "drive", "drive@example.test", app.encrypt("access"), "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO upload_sessions (id,user_id,target_connected_account_id,file_name,mime_type,size_bytes,status,google_session_uri) VALUES (?,?,?,?,?,?,?,?)`, "session", user.ID, "account", "test.txt", "text/plain", 10, "completed", app.encrypt("https://example.invalid"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/uploads/resumable/status/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var response struct {
		Status string `json:"status"`
		Offset string `json:"offset"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if response.Status != "completed" || response.Offset != "10" {
		t.Fatalf("response = %s", w.Body.String())
	}
}
