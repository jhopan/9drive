package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResumableStatusReadsGoogleOffset(t *testing.T) {
	app := newTestApp(t)
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("Content-Range") != "bytes */10" {
			t.Fatalf("unexpected probe: %s %q", r.Method, r.Header.Get("Content-Range"))
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Range", "bytes=0-4")
		w.WriteHeader(http.StatusPermanentRedirect)
	}))
	defer google.Close()
	token, user := registerAndLogin(t, app, "probe@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,access_token_encrypted,scopes) VALUES (?,?,?,?,?,?,?)`, "account", user.ID, "google_drive", "drive", "drive@example.test", app.encrypt("access"), "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO upload_sessions (id,user_id,target_connected_account_id,file_name,mime_type,size_bytes,status,google_session_uri) VALUES (?,?,?,?,?,?,?,?)`, "session", user.ID, "account", "test.txt", "text/plain", 10, "uploading", app.encrypt(google.URL))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/uploads/resumable/status/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != `{"offset":"5","status":"uploading"}`+"\n" {
		t.Fatalf("response = %d %s", w.Code, w.Body.String())
	}
}

func TestResumableStatusRejectsOtherUsersSession(t *testing.T) {
	app := newTestApp(t)
	token, _ := registerAndLogin(t, app, "probe-a@example.test")
	_, other := registerAndLogin(t, app, "probe-b@example.test")
	_, err := app.DB.Exec(`INSERT INTO upload_sessions (id,user_id,file_name,mime_type,size_bytes,status,google_session_uri) VALUES (?,?,?,?,?,?,?)`, "other", other.ID, "test.txt", "text/plain", 1, "uploading", app.encrypt("https://example.invalid"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/uploads/resumable/status/other", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}
