package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResumableInitCreatesGoogleSession(t *testing.T) {
	app := newTestApp(t)
	googleAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload/drive/v3/files" || r.URL.Query().Get("uploadType") != "resumable" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Location", "https://upload.example/session/abc")
		w.WriteHeader(http.StatusOK)
	}))
	defer googleAPI.Close()
	app.GoogleUploadAPIURL = googleAPI.URL + "/upload/drive/v3/files"
	token, user := registerAndLogin(t, app, "resumable@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,access_token_encrypted,refresh_token_encrypted,token_expires_at,scopes) VALUES (?,?,?,?,?,?,?,?,?)`, "account", user.ID, "google_drive", "drive", "drive@example.test", app.encrypt("access"), app.encrypt("refresh"), "2099-01-01T00:00:00Z", "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO storage_accounts (id,connected_account_id,total_bytes,used_bytes,available_bytes) VALUES (?,?,?,?,?)`, "storage", "account", 1000, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/uploads/resumable/init", strings.NewReader(`{"fileName":"test.txt","mimeType":"text/plain","sizeBytes":"3"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.SessionID == "" {
		t.Fatal("missing session id")
	}
}
