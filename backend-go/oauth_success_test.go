package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestGoogleCallbackStoresConnectedAccount(t *testing.T) {
	app := newTestApp(t)
	oauthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-token", "refresh_token": "refresh-token", "token_type": "Bearer", "expires_in": 3600})
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-token" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "google-id", "email": "drive@example.test", "name": "Drive Test"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer oauthServer.Close()
	base, _ := url.Parse(oauthServer.URL)
	app.GoogleEndpoint = oauth2.Endpoint{AuthURL: oauthServer.URL + "/auth", TokenURL: oauthServer.URL + "/token"}
	app.GoogleUserInfoURL = oauthServer.URL + "/userinfo"

	token, user := registerAndLogin(t, app, "oauth-success@example.test")
	config := httptest.NewRequest(http.MethodPost, "/system/google-config", strings.NewReader(`{"clientId":"client","clientSecret":"secret","redirectUri":"http://localhost:4000/connected-accounts/google/callback"}`))
	config.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, config)
	if w.Code != http.StatusCreated {
		t.Fatalf("config = %d: %s", w.Code, w.Body.String())
	}

	connect := httptest.NewRequest(http.MethodGet, "/connected-accounts/google/connect-url", nil)
	connect.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, connect)
	var connectResult struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &connectResult); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(connectResult.URL)
	if err != nil {
		t.Fatal(err)
	}
	state := authURL.Query().Get("state")
	if state == "" {
		t.Fatal("missing OAuth state")
	}

	callback := httptest.NewRequest(http.MethodGet, "/connected-accounts/google/callback?code=code&state="+url.QueryEscape(state), nil)
	w = httptest.NewRecorder()
	app.Router().ServeHTTP(w, callback)
	if w.Code != http.StatusFound || w.Header().Get("Location") != "http://localhost:5173/google-connected?status=success" {
		t.Fatalf("callback = %d %q", w.Code, w.Header().Get("Location"))
	}

	var email, encryptedRefresh, usedAt string
	if err := app.DB.QueryRow(`SELECT email,refresh_token_encrypted FROM connected_accounts WHERE user_id=? AND provider_account_id='google-id'`, user.ID).Scan(&email, &encryptedRefresh); err != nil {
		t.Fatal(err)
	}
	if email != "drive@example.test" {
		t.Fatalf("email = %q", email)
	}
	if encryptedRefresh == "refresh-token" {
		t.Fatal("refresh token stored plaintext")
	}
	if err := app.DB.QueryRow(`SELECT used_at FROM oauth_states WHERE user_id=?`, user.ID).Scan(&usedAt); err != nil || usedAt == "" {
		t.Fatalf("oauth state not marked used: %v", err)
	}
	_ = base
}
