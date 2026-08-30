package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUploadTargetChoosesMostAvailableAccount(t *testing.T) {
	app := newTestApp(t)
	token, user := registerAndLogin(t, app, "routing@example.test")
	for _, account := range []struct {
		id, providerID string
		available      int64
	}{
		{"small", "g-small", 100}, {"large", "g-large", 900},
	} {
		_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,scopes) VALUES (?,?,?,?,?,?)`, account.id, user.ID, "google_drive", account.providerID, account.id+"@example.test", "[]")
		if err != nil {
			t.Fatal(err)
		}
		_, err = app.DB.Exec(`INSERT INTO storage_accounts (id,connected_account_id,total_bytes,used_bytes,available_bytes) VALUES (?,?,?,?,?)`, "s-"+account.id, account.id, 1000, 1000-account.available, account.available)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/uploads/target", newJSONBody(t, map[string]string{"sizeBytes": "200"}))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := jsonField(t, w.Body.Bytes(), "accountId"); got != "large" {
		t.Fatalf("accountId = %q", got)
	}
}

func TestUploadTargetRejectsWhenNoAccountHasSpace(t *testing.T) {
	app := newTestApp(t)
	token, user := registerAndLogin(t, app, "no-space@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,scopes) VALUES (?,?,?,?,?,?)`, "full", user.ID, "google_drive", "g-full", "full@example.test", "[]")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.DB.Exec(`INSERT INTO storage_accounts (id,connected_account_id,total_bytes,used_bytes,available_bytes) VALUES (?,?,?,?,?)`, "s-full", "full", 100, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/uploads/target", newJSONBody(t, map[string]string{"sizeBytes": "1"}))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
}
