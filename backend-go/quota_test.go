package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSyncGoogleQuotaStoresQuota(t *testing.T) {
	app := newTestApp(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/drive/v3/about" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"storageQuota": map[string]string{"limit": "1000", "usage": "250", "usageInDriveTrash": "20"}})
	}))
	defer api.Close()
	app.GoogleDriveAPIURL = api.URL + "/drive/v3"

	token, user := registerAndLogin(t, app, "quota@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,access_token_encrypted,refresh_token_encrypted,token_expires_at,scopes) VALUES (?,?,?,?,?,?,?,?,?)`, "account", user.ID, "google_drive", "google", "drive@example.test", app.encrypt("access"), app.encrypt("refresh"), "2099-01-01T00:00:00Z", "[]")
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/connected-accounts/account/sync-quota", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Quota struct {
			TotalBytes     string `json:"totalBytes"`
			UsedBytes      string `json:"usedBytes"`
			AvailableBytes string `json:"availableBytes"`
			TrashBytes     string `json:"trashBytes"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Quota.TotalBytes != "1000" || response.Quota.UsedBytes != "250" || response.Quota.AvailableBytes != "750" || response.Quota.TrashBytes != "20" {
		t.Fatalf("quota = %#v", response.Quota)
	}
}

func TestSyncQuotaRejectsAnotherUsersAccount(t *testing.T) {
	app := newTestApp(t)
	token, _ := registerAndLogin(t, app, "quota-one@example.test")
	_, other := registerAndLogin(t, app, "quota-two@example.test")
	_, err := app.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider,provider_account_id,email,scopes) VALUES (?,?,?,?,?,?)`, "other-account", other.ID, "google_drive", "google", "other@example.test", "[]")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/connected-accounts/other-account/sync-quota", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}
