package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshReturnsNewAccessToken(t *testing.T) {
	app := newTestApp(t)
	r := app.Router()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(`{"name":"Refresh","email":"refresh@example.test","password":"password-123"}`)))
	var session struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewBufferString(`{"refreshToken":"`+session.RefreshToken+`"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("refresh = %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AccessToken == "" {
		t.Fatal("missing refreshed token")
	}
}

func TestLogoutRevokesRefreshSessions(t *testing.T) {
	app := newTestApp(t)
	r := app.Router()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(`{"name":"Logout","email":"logout@example.test","password":"password-123"}`)))
	var session struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+session.AccessToken)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("logout = %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewBufferString(`{"refreshToken":"`+session.RefreshToken+`"}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d", w.Code)
	}
}
