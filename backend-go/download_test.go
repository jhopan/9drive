package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDownloadStreamsGoogleFileWithRange(t *testing.T) {
	app := newTestApp(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive/v3/files/google-file" || r.URL.Query().Get("alt") != "media" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Range") != "bytes=2-4" {
			t.Errorf("range = %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Range", "bytes 2-4/6")
		w.Header().Set("Content-Length", "3")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("cde"))
	}))
	defer api.Close()
	app.GoogleDriveAPIURL = api.URL + "/drive/v3"
	token, user := registerAndLogin(t, app, "download@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,access_token_encrypted,refresh_token_encrypted,token_expires_at,scopes) VALUES (?,?,?,?,?,?,?,?,?)`, "account", user.ID, "google_drive", "google", "drive@example.test", app.encrypt("access"), app.encrypt("refresh"), "2099-01-01T00:00:00Z", "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,provider,provider_file_id,name,mime_type,size_bytes) VALUES (?,?,?,?,?,?,?,?)`, "file", user.ID, "account", "google_drive", "google-file", "movie.mp4", "video/mp4", 6)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/files/file/download", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "cde" {
		t.Fatalf("body = %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="movie.mp4"` {
		t.Fatalf("disposition = %q", got)
	}
	if got := w.Header().Get("Content-Range"); got != "bytes 2-4/6" {
		t.Fatalf("content-range = %q", got)
	}
}

func TestDownloadCannotAccessOtherUsersFile(t *testing.T) {
	app := newTestApp(t)
	token, _ := registerAndLogin(t, app, "download-a@example.test")
	_, other := registerAndLogin(t, app, "download-b@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,scopes) VALUES (?,?,?,?,?,?)`, "account-b", other.ID, "google_drive", "google-b", "b@example.test", "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,provider,provider_file_id,name,mime_type,size_bytes) VALUES (?,?,?,?,?,?,?,?)`, "file-b", other.ID, "account-b", "google_drive", "other", "secret.txt", "text/plain", 1)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/files/file-b/download", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}
