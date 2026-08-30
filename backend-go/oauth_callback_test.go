package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGoogleCallbackRejectsUnknownState(t *testing.T) {
	app := newTestApp(t)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/connected-accounts/google/callback?code=code&state=unknown", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "http://localhost:5173/google-connected?status=error" {
		t.Fatalf("location = %q", got)
	}
}

func TestGoogleCallbackRejectsExpiredState(t *testing.T) {
	app := newTestApp(t)
	_, user := registerAndLogin(t, app, "expired-oauth@example.test")
	_, err := app.DB.Exec(`INSERT INTO provider_configs (id,user_id,provider,client_id_encrypted,client_secret_encrypted,redirect_uri,scopes,status) VALUES (?,?,?,?,?,?,?,?)`, "config", user.ID, "google_drive", app.encrypt("client"), app.encrypt("secret"), "http://localhost:4000/connected-accounts/google/callback", `[]`, "active")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO oauth_states (id,user_id,provider_config_id,flow,state_hash,expires_at) VALUES (?,?,?,?,?,?)`, "state", user.ID, "config", "connect", hashToken("expired"), "2000-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/connected-accounts/google/callback?code=code&state=expired", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Header().Get("Location"); got != "http://localhost:5173/google-connected?status=error" {
		t.Fatalf("location = %q", got)
	}
}
