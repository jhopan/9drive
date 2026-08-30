package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncGoogleFilesUpsertsMetadata(t *testing.T) {
	app := newTestApp(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive/v3/files" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]string{
			{"id": "google-file", "name": "movie.mp4", "mimeType": "video/mp4", "size": "2048", "createdTime": "2026-08-04T00:00:00Z", "modifiedTime": "2026-08-04T01:00:00Z"},
		}})
	}))
	defer api.Close()
	app.GoogleDriveAPIURL = api.URL + "/drive/v3"

	token, user := registerAndLogin(t, app, "sync@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,access_token_encrypted,refresh_token_encrypted,token_expires_at,scopes) VALUES (?,?,?,?,?,?,?,?,?)`, "account", user.ID, "google_drive", "google", "drive@example.test", app.encrypt("access"), app.encrypt("refresh"), "2099-01-01T00:00:00Z", "[]")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/files/sync-google", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sync = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Results []struct {
			Created int `json:"created"`
			Updated int `json:"updated"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Created != 1 {
		t.Fatalf("results = %#v", response.Results)
	}

	var name string
	var size int64
	if err := app.DB.QueryRow(`SELECT name,size_bytes FROM files WHERE provider_file_id='google-file'`).Scan(&name, &size); err != nil {
		t.Fatal(err)
	}
	if name != "movie.mp4" || size != 2048 {
		t.Fatalf("file = %q %d", name, size)
	}
}

func TestSyncGoogleFilesCannotSyncAnotherAccount(t *testing.T) {
	app := newTestApp(t)
	token, _ := registerAndLogin(t, app, "sync-one@example.test")
	_, other := registerAndLogin(t, app, "sync-two@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,scopes) VALUES (?,?,?,?,?,?)`, "other", other.ID, "google_drive", "google", "other@example.test", "[]")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/files/sync-google?connectedAccountId=other", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var response struct {
		Results []any `json:"results"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if len(response.Results) != 0 {
		t.Fatalf("results = %#v", response.Results)
	}
}
