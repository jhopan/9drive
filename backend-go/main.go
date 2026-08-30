package main

import (
	"archive/zip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	_ "modernc.org/sqlite"
)

type Config struct {
	DatabaseURL string
	AppPort     string
	FrontendURL string
	JWTSecret   string
	TokenKey    string
}

type App struct {
	DB                 *sql.DB
	Config             Config
	HTTPClient         *http.Client
	GoogleEndpoint     oauth2.Endpoint
	GoogleUserInfoURL  string
	GoogleDriveAPIURL  string
	GoogleUploadAPIURL string
}

type authUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type ctxKey string

const userKey ctxKey = "user"

func loadConfig() Config {
	_ = godotenv.Load()
	getenv := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	return Config{
		DatabaseURL: getenv("DATABASE_URL", "file:data/9drive.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"),
		AppPort:     getenv("APP_PORT", "4000"),
		FrontendURL: getenv("FRONTEND_URL", "http://localhost:5173"),
		JWTSecret:   getenv("JWT_ACCESS_SECRET", "change-this-jwt-secret-before-production"),
		TokenKey:    getenv("TOKEN_ENCRYPTION_KEY", "change-this-token-key-before-production"),
	}
}

func (a *App) migrate() error {
	_, err := a.DB.Exec(`
PRAGMA foreign_keys = ON;
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS user_sessions (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, refresh_token_hash TEXT NOT NULL,
  expires_at TEXT NOT NULL, revoked_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS user_sessions_user_id_idx ON user_sessions(user_id);
CREATE TABLE IF NOT EXISTS provider_configs (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, provider TEXT NOT NULL,
  client_id_encrypted TEXT NOT NULL, client_secret_encrypted TEXT NOT NULL,
  redirect_uri TEXT NOT NULL, scopes TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS oauth_states (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, provider_config_id TEXT NOT NULL,
  flow TEXT NOT NULL DEFAULT 'connect', state_hash TEXT NOT NULL UNIQUE, expires_at TEXT NOT NULL,
  used_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY(provider_config_id) REFERENCES provider_configs(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS connected_accounts (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, provider_config_id TEXT,
  provider TEXT NOT NULL DEFAULT 'google_drive', provider_account_id TEXT NOT NULL,
  email TEXT NOT NULL, display_name TEXT, avatar_url TEXT, access_token_encrypted TEXT,
  refresh_token_encrypted TEXT, token_expires_at TEXT, scopes TEXT NOT NULL DEFAULT '[]',
  status TEXT NOT NULL DEFAULT 'connected', last_error TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE(user_id, provider, provider_account_id),
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY(provider_config_id) REFERENCES provider_configs(id) ON DELETE SET NULL
);
CREATE TABLE IF NOT EXISTS storage_accounts (
  id TEXT PRIMARY KEY, connected_account_id TEXT NOT NULL UNIQUE, total_bytes INTEGER,
  used_bytes INTEGER NOT NULL DEFAULT 0, available_bytes INTEGER, trash_bytes INTEGER,
  last_synced_at TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(connected_account_id) REFERENCES connected_accounts(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS upload_routing_policies (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL UNIQUE, mode TEXT NOT NULL DEFAULT 'most_available',
  priority_account_ids TEXT NOT NULL DEFAULT '[]', round_robin_cursor INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS folders (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, parent_id TEXT, connected_account_id TEXT,
  provider TEXT NOT NULL DEFAULT 'google_drive', provider_folder_id TEXT, name TEXT NOT NULL,
  color TEXT NOT NULL DEFAULT 'text-blue-500', icon_url TEXT, deleted_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY(parent_id) REFERENCES folders(id) ON DELETE SET NULL,
  FOREIGN KEY(connected_account_id) REFERENCES connected_accounts(id) ON DELETE SET NULL
);
CREATE TABLE IF NOT EXISTS files (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, connected_account_id TEXT NOT NULL, folder_id TEXT,
  provider TEXT NOT NULL DEFAULT 'google_drive', provider_file_id TEXT NOT NULL, name TEXT NOT NULL,
  mime_type TEXT NOT NULL, size_bytes INTEGER NOT NULL, checksum TEXT,
  status TEXT NOT NULL DEFAULT 'active', deleted_at TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY(connected_account_id) REFERENCES connected_accounts(id) ON DELETE CASCADE,
  FOREIGN KEY(folder_id) REFERENCES folders(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS files_user_folder_idx ON files(user_id, status, folder_id, created_at);
CREATE TABLE IF NOT EXISTS upload_sessions (
  id TEXT PRIMARY KEY, user_id TEXT NOT NULL, target_connected_account_id TEXT, folder_id TEXT,
  file_name TEXT NOT NULL, mime_type TEXT NOT NULL, size_bytes INTEGER NOT NULL, status TEXT NOT NULL,
  google_session_uri TEXT, error_message TEXT, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, completed_at TEXT,
  FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
  FOREIGN KEY(target_connected_account_id) REFERENCES connected_accounts(id) ON DELETE SET NULL,
  FOREIGN KEY(folder_id) REFERENCES folders(id) ON DELETE SET NULL
);`)
	return err
}

func (a *App) ensureInitialAdmin() error {
	var count int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err != nil { return err }
	if count != 0 { return nil }
	hash, err := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
	if err != nil { return err }
	_, err = a.DB.Exec(`INSERT INTO users (id,name,email,password_hash) VALUES (?,?,?,?)`, randomID(), "Administrator", "admin@gmail.com", string(hash))
	return err
}

func (a *App) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/refresh", a.refresh)
	mux.HandleFunc("POST /auth/logout", a.requireAuth(a.logout))
	mux.HandleFunc("GET /auth/me", a.requireAuth(a.me))
	mux.HandleFunc("PUT /auth/me", a.requireAuth(a.updateMe))
	mux.HandleFunc("GET /system/google-config", a.requireAuth(a.getGoogleConfig))
	mux.HandleFunc("POST /system/google-config", a.requireAuth(a.saveGoogleConfig))
	mux.HandleFunc("POST /system/update", a.requireAuth(a.systemUpdate))
	mux.HandleFunc("GET /connected-accounts", a.requireAuth(a.listAccounts))
	mux.HandleFunc("GET /connected-accounts/google/connect-url", a.requireAuth(a.googleConnectURL))
	mux.HandleFunc("GET /connected-accounts/google/callback", a.googleCallback)
	mux.HandleFunc("GET /storage/summary", a.requireAuth(a.storageSummary))
	mux.HandleFunc("GET /storage/breakdown", a.requireAuth(a.storageBreakdown))
	mux.HandleFunc("GET /storage/routing-policy", a.requireAuth(a.getRoutingPolicy))
	mux.HandleFunc("PATCH /storage/routing-policy", a.requireAuth(a.updateRoutingPolicy))
	mux.HandleFunc("POST /connected-accounts/{id}/sync-quota", a.requireAuth(a.syncQuota))
	mux.HandleFunc("GET /folders", a.requireAuth(a.listFolders))
	mux.HandleFunc("POST /folders", a.requireAuth(a.createFolder))
	mux.HandleFunc("GET /files", a.requireAuth(a.listFiles))
	mux.HandleFunc("POST /files/sync-google", a.requireAuth(a.syncGoogleFiles))
	mux.HandleFunc("GET /files/{id}/view-url", a.requireAuth(a.viewFileUrl))
	mux.HandleFunc("GET /files/{id}/download", a.requireAuth(a.downloadFile))
	mux.HandleFunc("POST /files/{id}/share", a.requireAuth(a.shareFileUrl))
	mux.HandleFunc("POST /files/{id}/public-permission", a.requireAuth(a.publicPermission))
	mux.HandleFunc("POST /files/batch-download", a.requireAuth(a.batchDownloadZip))
	mux.HandleFunc("PATCH /files/{id}", a.requireAuth(a.updateFile))
	mux.HandleFunc("DELETE /files/{id}", a.requireAuth(a.deleteFile))
	mux.HandleFunc("PATCH /files/batch", a.requireAuth(a.batchUpdateFiles))
	mux.HandleFunc("DELETE /files/batch", a.requireAuth(a.batchDeleteFiles))
	mux.HandleFunc("PATCH /folders/{id}", a.requireAuth(a.updateFolder))
	mux.HandleFunc("DELETE /folders/{id}", a.requireAuth(a.deleteFolder))
	mux.HandleFunc("POST /uploads/target", a.requireAuth(a.selectUploadTarget))
	mux.HandleFunc("POST /uploads/resumable/init", a.requireAuth(a.initResumableUpload))
	mux.HandleFunc("GET /uploads/resumable/status/{id}", a.requireAuth(a.resumableStatus))
	mux.HandleFunc("PUT /uploads/resumable/chunk/{id}", a.requireAuth(a.resumableChunk))
	mux.HandleFunc("/", jsonNotFound)
	return a.cors(mux)
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var count int
	if err := a.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&count); err == nil && count > 0 {
		writeError(w, http.StatusForbidden, "REGISTRATION_DISABLED", "Registration is disabled. Use the initial administrator account.")
		return
	}

	var body struct{ Name, Email, Password string }
	if err := decodeJSON(r, &body); err != nil || body.Email == "" || body.Password == "" || body.Name == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Name, email, and password are required.")
		return
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(body.Password), 10)
	user := authUser{ID: randomID(), Name: body.Name, Email: body.Email}
	_, err := a.DB.Exec(`INSERT INTO users (id,name,email,password_hash) VALUES (?,?,?,?)`, user.ID, user.Name, user.Email, hash)
	if err != nil {
		writeError(w, http.StatusConflict, "EMAIL_IN_USE", "Email already registered.")
		return
	}
	a.respondSession(w, http.StatusCreated, user)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var body struct{ Email, Password string }
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	var user authUser
	var hash string
	err := a.DB.QueryRow(`SELECT id,name,email,password_hash FROM users WHERE email = ? AND status = 'active'`, strings.ToLower(strings.TrimSpace(body.Email))).Scan(&user.ID, &user.Name, &user.Email, &hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(body.Password)) != nil {
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password.")
		return
	}
	a.respondSession(w, http.StatusOK, user)
}


func (a *App) respondSession(w http.ResponseWriter, status int, user authUser) {
	accessToken, err := a.signAccessToken(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_FAILED", "Unable to create session.")
		return
	}
	refreshToken := randomToken()
	_, err = a.DB.Exec(`INSERT INTO user_sessions (id,user_id,refresh_token_hash,expires_at) VALUES (?,?,?,?)`, randomID(), user.ID, hashToken(refreshToken), time.Now().Add(30*24*time.Hour).UTC().Format(time.RFC3339Nano))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_FAILED", "Unable to create session.")
		return
	}
	writeJSON(w, status, map[string]any{"accessToken": accessToken, "refreshToken": refreshToken, "user": user})
}

func (a *App) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(r, &body); err != nil || body.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Refresh token is required.")
		return
	}
	var user authUser
	err := a.DB.QueryRow(`SELECT u.id,u.name,u.email FROM user_sessions s JOIN users u ON u.id=s.user_id WHERE s.refresh_token_hash=? AND s.revoked_at IS NULL AND s.expires_at > ?`, hashToken(body.RefreshToken), time.Now().UTC().Format(time.RFC3339Nano)).Scan(&user.ID, &user.Name, &user.Email)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "AUTH_SESSION_EXPIRED", "Refresh token expired.")
		return
	}
	token, err := a.signAccessToken(user)
	if err != nil {
		writeError(w, 500, "TOKEN_FAILED", "Unable to refresh session.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"accessToken": token})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	_, _ = a.DB.Exec(`UPDATE user_sessions SET revoked_at=? WHERE user_id=? AND revoked_at IS NULL`, time.Now().UTC().Format(time.RFC3339Nano), user.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) getGoogleConfig(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	redirect := "http://" + r.Host + "/connected-accounts/google/callback"
	var configuredRedirect string
	err := a.DB.QueryRow(`SELECT redirect_uri FROM provider_configs WHERE user_id=? AND provider='google_drive' AND status='active' ORDER BY created_at DESC LIMIT 1`, user.ID).Scan(&configuredRedirect)
	writeJSON(w, http.StatusOK, map[string]any{"exists": err == nil, "clientId": "", "redirectUri": configuredRedirect, "hasSecret": err == nil, "defaultRedirectUri": redirect})
}

func (a *App) saveGoogleConfig(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		RedirectURI  string `json:"redirectUri"`
	}
	if err := decodeJSON(r, &body); err != nil || body.ClientID == "" || body.ClientSecret == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Client ID and Client Secret are required.")
		return
	}
	if body.RedirectURI == "" {
		body.RedirectURI = "http://" + r.Host + "/connected-accounts/google/callback"
	}
	if _, err := url.ParseRequestURI(body.RedirectURI); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid redirect URI.")
		return
	}
	_, _ = a.DB.Exec(`UPDATE provider_configs SET status='disabled' WHERE user_id=? AND provider='google_drive' AND status='active'`, user.ID)
	_, err := a.DB.Exec(`INSERT INTO provider_configs (id,user_id,provider,client_id_encrypted,client_secret_encrypted,redirect_uri,scopes,status) VALUES (?,?,?,?,?,?,?, 'active')`, randomID(), user.ID, "google_drive", a.encrypt(body.ClientID), a.encrypt(body.ClientSecret), body.RedirectURI, `["https://www.googleapis.com/auth/drive","https://www.googleapis.com/auth/userinfo.email","https://www.googleapis.com/auth/userinfo.profile"]`)
	if err != nil {
		writeError(w, 500, "GOOGLE_CONFIG_FAILED", "Unable to save Google config.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"message": "Google OAuth credentials saved."})
}

func (a *App) systemUpdate(w http.ResponseWriter, r *http.Request) {
	go func() {
		exec.Command("git", "pull", "origin", "main").Run()
	}()
	writeJSON(w, http.StatusOK, map[string]string{"message": "System update initiated"})
}

func (a *App) googleConnectURL(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var id, encryptedID, redirectURI, scopes string
	err := a.DB.QueryRow(`SELECT id,client_id_encrypted,redirect_uri,scopes FROM provider_configs WHERE user_id=? AND provider='google_drive' AND status='active' ORDER BY created_at DESC LIMIT 1`, user.ID).Scan(&id, &encryptedID, &redirectURI, &scopes)
	if err != nil {
		writeError(w, http.StatusBadRequest, "GOOGLE_NOT_CONFIGURED", "Configure Google OAuth first.")
		return
	}
	clientID, err := a.decrypt(encryptedID)
	if err != nil {
		writeError(w, 500, "GOOGLE_CONFIG_FAILED", "Unable to read Google config.")
		return
	}
	state := randomToken()
	_, err = a.DB.Exec(`INSERT INTO oauth_states (id,user_id,provider_config_id,flow,state_hash,expires_at) VALUES (?,?,?,?,?,?)`, randomID(), user.ID, id, "connect", hashToken(state), time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339Nano))
	if err != nil {
		writeError(w, 500, "OAUTH_STATE_FAILED", "Unable to create OAuth session.")
		return
	}
	values := url.Values{"client_id": {clientID}, "redirect_uri": {redirectURI}, "response_type": {"code"}, "access_type": {"offline"}, "prompt": {"consent"}, "include_granted_scopes": {"true"}, "scope": {strings.Join(parseScopes(scopes), " ")}, "state": {state}}
	writeJSON(w, http.StatusOK, map[string]string{"url": "https://accounts.google.com/o/oauth2/v2/auth?" + values.Encode()})
}

func (a *App) listAccounts(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	rows, err := a.DB.Query(`SELECT c.id,c.provider,c.email,COALESCE(c.display_name,''),c.status,COALESCE(s.total_bytes,0),COALESCE(s.used_bytes,0),COALESCE(s.available_bytes,0),COALESCE(s.last_synced_at,'') FROM connected_accounts c LEFT JOIN storage_accounts s ON s.connected_account_id=c.id WHERE c.user_id=? AND c.provider='google_drive' AND c.status='connected' ORDER BY c.created_at DESC`, user.ID)
	if err != nil {
		writeError(w, 500, "ACCOUNTS_FAILED", "Unable to list connected accounts.")
		return
	}
	defer rows.Close()
	accounts := make([]map[string]any, 0)
	for rows.Next() {
		var id, provider, email, displayName, status, lastSynced string
		var total, used, available int64
		if err := rows.Scan(&id, &provider, &email, &displayName, &status, &total, &used, &available, &lastSynced); err != nil {
			writeError(w, 500, "ACCOUNTS_FAILED", "Unable to read connected accounts.")
			return
		}
		accounts = append(accounts, map[string]any{"id": id, "provider": provider, "email": email, "displayName": displayName, "status": status, "storageAccount": map[string]string{"totalBytes": fmt.Sprint(total), "usedBytes": fmt.Sprint(used), "availableBytes": fmt.Sprint(available), "lastSyncedAt": lastSynced}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (a *App) storageSummary(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var total, used, available int64
	err := a.DB.QueryRow(`SELECT COALESCE(SUM(s.total_bytes),0),COALESCE(SUM(s.used_bytes),0),COALESCE(SUM(s.available_bytes),0) FROM connected_accounts c LEFT JOIN storage_accounts s ON s.connected_account_id=c.id WHERE c.user_id=? AND c.status='connected'`, user.ID).Scan(&total, &used, &available)
	if err != nil {
		writeError(w, 500, "STORAGE_FAILED", "Unable to calculate storage.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"totalBytes": fmt.Sprint(total), "usedBytes": fmt.Sprint(used), "availableBytes": fmt.Sprint(available)})
}

func (a *App) storageBreakdown(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var photo, video, doc int64
	rows, err := a.DB.Query(`SELECT mime_type, COALESCE(SUM(size_bytes), 0) FROM files WHERE user_id=? AND status='active' GROUP BY mime_type`, user.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var mime string
			var size int64
			if err := rows.Scan(&mime, &size); err == nil {
				if strings.HasPrefix(mime, "image/") {
					photo += size
				} else if strings.HasPrefix(mime, "video/") {
					video += size
				} else {
					doc += size
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"photo":    fmt.Sprint(photo),
		"video":    fmt.Sprint(video),
		"document": fmt.Sprint(doc),
	})
}

func (a *App) getRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var mode, p string
	err := a.DB.QueryRow(`SELECT mode, priority_account_ids FROM upload_routing_policies WHERE user_id=?`, user.ID).Scan(&mode, &p)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusOK, map[string]any{"policy": map[string]any{"mode": "most_available", "priorityAccountIds": []string{}}})
		return
	}
	var accs []string
	json.Unmarshal([]byte(p), &accs)
	writeJSON(w, http.StatusOK, map[string]any{"policy": map[string]any{"mode": mode, "priorityAccountIds": accs}})
}

func (a *App) updateRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		Mode               string   `json:"mode"`
		PriorityAccountIds []string `json:"priorityAccountIds"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid request body")
		return
	}
	accBytes, _ := json.Marshal(body.PriorityAccountIds)
	_, _ = a.DB.Exec(`INSERT INTO upload_routing_policies (id, user_id, mode, priority_account_ids) VALUES (?, ?, ?, ?) ON CONFLICT(user_id) DO UPDATE SET mode=excluded.mode, priority_account_ids=excluded.priority_account_ids`, randomID(), user.ID, body.Mode, string(accBytes))
	writeJSON(w, http.StatusOK, map[string]any{"policy": body})
}

func (a *App) getGoogleToken(ctx context.Context, accountID string, forceRefresh bool) (string, error) {
	var encryptedToken, encryptedRefresh, expiresAt, configID string
	err := a.DB.QueryRow(`SELECT access_token_encrypted, refresh_token_encrypted, token_expires_at, provider_config_id FROM connected_accounts WHERE id=?`, accountID).Scan(&encryptedToken, &encryptedRefresh, &expiresAt, &configID)
	if err != nil {
		return "", err
	}

	exp, err := time.Parse(time.RFC3339Nano, expiresAt)
	if !forceRefresh && err == nil && exp.After(time.Now().Add(5*time.Minute)) {
		return a.decrypt(encryptedToken)
	}

	// Token expired or forced refresh
	refreshToken, err := a.decrypt(encryptedRefresh)
	if err != nil || refreshToken == "" {
		return "", errors.New("refresh token missing or invalid")
	}

	var encryptedClientID, encryptedClientSecret string
	err = a.DB.QueryRow(`SELECT client_id_encrypted, client_secret_encrypted FROM provider_configs WHERE id=?`, configID).Scan(&encryptedClientID, &encryptedClientSecret)
	if err != nil {
		return "", err
	}
	clientID, _ := a.decrypt(encryptedClientID)
	clientSecret, _ := a.decrypt(encryptedClientSecret)

	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     a.GoogleEndpoint,
	}

	token := &oauth2.Token{RefreshToken: refreshToken}
	tokenSource := conf.TokenSource(ctx, token)
	newToken, err := tokenSource.Token()
	if err != nil {
		return "", err
	}

	// Update new token in DB
	newEncryptedRefresh := encryptedRefresh
	if newToken.RefreshToken != "" && newToken.RefreshToken != refreshToken {
		newEncryptedRefresh = a.encrypt(newToken.RefreshToken)
	}
	
	_, _ = a.DB.Exec(`UPDATE connected_accounts SET access_token_encrypted=?, refresh_token_encrypted=?, token_expires_at=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		a.encrypt(newToken.AccessToken), newEncryptedRefresh, newToken.Expiry.UTC().Format(time.RFC3339Nano), accountID)

	return newToken.AccessToken, nil
}

func (a *App) syncQuota(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	accountID := r.PathValue("id")
	var ownerID string
	err := a.DB.QueryRow(`SELECT user_id FROM connected_accounts WHERE id=?`, accountID).Scan(&ownerID)
	if err != nil || ownerID != user.ID {
		writeError(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Account not found.")
		return
	}
	accessToken, err := a.getGoogleToken(r.Context(), accountID, false)
	if err != nil {
		writeError(w, 500, "QUOTA_FAILED", "Unable to read account token.")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.GoogleDriveAPIURL+`/about?fields=storageQuota`, nil)
	if err != nil {
		writeError(w, 500, "QUOTA_FAILED", "Unable to create Drive request.")
		return
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := a.HTTPClient.Do(request)
	if err != nil {
		writeError(w, 502, "GOOGLE_UNAVAILABLE", "Google Drive quota request failed.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeError(w, 502, "GOOGLE_QUOTA_FAILED", "Google Drive rejected quota request.")
		return
	}
	var payload struct {
		StorageQuota struct {
			Limit string `json:"limit"`
			Usage string `json:"usage"`
			Trash string `json:"usageInDriveTrash"`
		} `json:"storageQuota"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		writeError(w, 502, "GOOGLE_QUOTA_FAILED", "Invalid Google Drive quota response.")
		return
	}
	var total, used, trash int64
	_, _ = fmt.Sscan(payload.StorageQuota.Limit, &total)
	_, _ = fmt.Sscan(payload.StorageQuota.Usage, &used)
	_, _ = fmt.Sscan(payload.StorageQuota.Trash, &trash)
	available := total - used
	if available < 0 {
		available = 0
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = a.DB.Exec(`INSERT INTO storage_accounts (id,connected_account_id,total_bytes,used_bytes,available_bytes,trash_bytes,last_synced_at) VALUES (?,?,?,?,?,?,?) ON CONFLICT(connected_account_id) DO UPDATE SET total_bytes=excluded.total_bytes,used_bytes=excluded.used_bytes,available_bytes=excluded.available_bytes,trash_bytes=excluded.trash_bytes,last_synced_at=excluded.last_synced_at`, randomID(), accountID, total, used, available, trash, now)
	if err != nil {
		writeError(w, 500, "QUOTA_FAILED", "Unable to save quota.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quota": map[string]string{"totalBytes": fmt.Sprint(total), "usedBytes": fmt.Sprint(used), "availableBytes": fmt.Sprint(available), "trashBytes": fmt.Sprint(trash)}})
}

func (a *App) createFolder(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		Name     string  `json:"name"`
		ParentID *string `json:"parentId"`
		Color    string  `json:"color"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Folder name is required.")
		return
	}
	if body.ParentID != nil {
		var exists int
		err := a.DB.QueryRow(`SELECT 1 FROM folders WHERE id=? AND user_id=? AND deleted_at IS NULL`, *body.ParentID, user.ID).Scan(&exists)
		if err != nil {
			writeError(w, http.StatusBadRequest, "PARENT_NOT_FOUND", "Parent folder not found.")
			return
		}
	}
	color := body.Color
	if color == "" {
		color = "text-blue-500"
	}
	id := randomID()
	_, err := a.DB.Exec(`INSERT INTO folders (id,user_id,parent_id,name,color) VALUES (?,?,?,?,?)`, id, user.ID, body.ParentID, strings.TrimSpace(body.Name), color)
	if err != nil {
		writeError(w, 500, "FOLDER_CREATE_FAILED", "Unable to create folder.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"folder": map[string]any{"id": id, "name": strings.TrimSpace(body.Name), "parentId": body.ParentID, "color": color}})
}

func (a *App) listFolders(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	parentID := r.URL.Query().Get("parentId")
	query := `SELECT id,name,parent_id,color,created_at,updated_at FROM folders WHERE user_id=? AND deleted_at IS NULL`
	args := []any{user.ID}
	if parentID == "" {
		query += ` AND parent_id IS NULL`
	} else {
		query += ` AND parent_id=?`
		args = append(args, parentID)
	}
	query += ` ORDER BY name COLLATE NOCASE`
	rows, err := a.DB.Query(query, args...)
	if err != nil {
		writeError(w, 500, "FOLDERS_FAILED", "Unable to list folders.")
		return
	}
	defer rows.Close()
	folders := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, color, createdAt, updatedAt string
		var parent sql.NullString
		if err := rows.Scan(&id, &name, &parent, &color, &createdAt, &updatedAt); err != nil {
			writeError(w, 500, "FOLDERS_FAILED", "Unable to read folders.")
			return
		}
		var parentID any
		if parent.Valid {
			parentID = parent.String
		}
		folders = append(folders, map[string]any{"id": id, "name": name, "parentId": parentID, "color": color, "createdAt": createdAt, "updatedAt": updatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders})
}

func (a *App) listFiles(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	folderID := r.URL.Query().Get("folderId")
	accountID := r.URL.Query().Get("accountId")
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	query := `SELECT f.id,f.name,f.mime_type,f.size_bytes,f.provider_file_id,f.folder_id,f.created_at,f.updated_at,c.id,c.email,c.provider,COALESCE(d.name,'') FROM files f JOIN connected_accounts c ON c.id=f.connected_account_id LEFT JOIN folders d ON d.id=f.folder_id WHERE f.user_id=? AND f.status='active'`
	args := []any{user.ID}
	if folderID != "" {
		query += ` AND f.folder_id=?`
		args = append(args, folderID)
	}
	if accountID != "" {
		query += ` AND f.connected_account_id=?`
		args = append(args, accountID)
	}
	if search != "" {
		query += ` AND f.name LIKE ?`
		args = append(args, "%"+search+"%")
	}
	query += ` ORDER BY f.created_at DESC`
	rows, err := a.DB.Query(query, args...)
	if err != nil {
		writeError(w, 500, "FILES_FAILED", "Unable to list files.")
		return
	}
	defer rows.Close()
	files := make([]map[string]any, 0)
	for rows.Next() {
		var id, name, mimeType, providerFileID, createdAt, updatedAt, accountID, email, provider, folderName string
		var size int64
		var folderID sql.NullString
		if err := rows.Scan(&id, &name, &mimeType, &size, &providerFileID, &folderID, &createdAt, &updatedAt, &accountID, &email, &provider, &folderName); err != nil {
			writeError(w, 500, "FILES_FAILED", "Unable to read files.")
			return
		}
		var folder any
		if folderID.Valid {
			folder = map[string]string{"id": folderID.String, "name": folderName}
		}
		files = append(files, map[string]any{"id": id, "name": name, "mimeType": mimeType, "sizeBytes": fmt.Sprint(size), "providerFileId": providerFileID, "folder": folder, "createdAt": createdAt, "updatedAt": updatedAt, "connectedAccount": map[string]string{"id": accountID, "email": email, "provider": provider}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (a *App) initResumableUpload(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		FileName        string  `json:"fileName"`
		MIMEType        string  `json:"mimeType"`
		SizeBytes       string  `json:"sizeBytes"`
		FolderID        *string `json:"folderId"`
		TargetAccountID string  `json:"targetAccountId"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.FileName) == "" {
		writeError(w, 400, "BAD_REQUEST", "File name is required.")
		return
	}
	var size int64
	_, err := fmt.Sscan(body.SizeBytes, &size)
	if err != nil || size <= 0 {
		writeError(w, 400, "BAD_REQUEST", "sizeBytes must be a positive integer.")
		return
	}
	mime := body.MIMEType
	if mime == "" {
		mime = "application/octet-stream"
	}
	account, err := a.selectAccountForUpload(user.ID, size, body.TargetAccountID)
	if err == sql.ErrNoRows {
		writeError(w, 400, "NO_ACCOUNT_WITH_ENOUGH_SPACE", "No connected Drive account has enough space.")
		return
	}
	if err != nil {
		writeError(w, 500, "UPLOAD_TARGET_FAILED", "Unable to select upload account.")
		return
	}
	var encryptedToken string
	err = a.DB.QueryRow(`SELECT access_token_encrypted FROM connected_accounts WHERE id=?`, account).Scan(&encryptedToken)
	if err != nil {
		writeError(w, 500, "UPLOAD_INIT_FAILED", "Unable to load Drive account.")
		return
	}
	accessToken, err := a.decrypt(encryptedToken)
	if err != nil {
		writeError(w, 500, "UPLOAD_INIT_FAILED", "Unable to read Drive token.")
		return
	}
	metadata, _ := json.Marshal(map[string]string{"name": body.FileName, "mimeType": mime})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.GoogleUploadAPIURL+`?uploadType=resumable`, strings.NewReader(string(metadata)))
	if err != nil {
		writeError(w, 500, "UPLOAD_INIT_FAILED", "Unable to create Google upload.")
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", mime)
	req.Header.Set("X-Upload-Content-Length", fmt.Sprint(size))
	response, err := a.HTTPClient.Do(req)
	if err != nil {
		writeError(w, 502, "GOOGLE_UNAVAILABLE", "Google upload init failed.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, 502, "GOOGLE_UPLOAD_INIT_FAILED", "Google rejected upload init.")
		return
	}
	googleSession := response.Header.Get("Location")
	if googleSession == "" {
		writeError(w, 502, "GOOGLE_UPLOAD_INIT_FAILED", "Google did not return upload session.")
		return
	}
	sessionID := randomID()
	_, err = a.DB.Exec(`INSERT INTO upload_sessions (id,user_id,target_connected_account_id,folder_id,file_name,mime_type,size_bytes,status,google_session_uri) VALUES (?,?,?,?,?,?,?,?,?)`, sessionID, user.ID, account, body.FolderID, body.FileName, mime, size, "uploading", a.encrypt(googleSession))
	if err != nil {
		writeError(w, 500, "UPLOAD_INIT_FAILED", "Unable to save upload session.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"sessionId": sessionID, "provider": "google_drive"})
}

func (a *App) resumableStatus(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var status, accountID, encryptedURI string
	var size int64
	err := a.DB.QueryRow(`SELECT status,target_connected_account_id,google_session_uri,size_bytes FROM upload_sessions WHERE id=? AND user_id=?`, r.PathValue("id"), user.ID).Scan(&status, &accountID, &encryptedURI, &size)
	if err == sql.ErrNoRows {
		writeError(w, 404, "UPLOAD_NOT_FOUND", "Upload session not found.")
		return
	}
	if err != nil {
		writeError(w, 500, "UPLOAD_STATUS_FAILED", "Unable to read upload session.")
		return
	}
	if status == "completed" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "completed", "offset": fmt.Sprint(size)})
		return
	}
	var encryptedToken string
	if err := a.DB.QueryRow(`SELECT access_token_encrypted FROM connected_accounts WHERE id=? AND user_id=?`, accountID, user.ID).Scan(&encryptedToken); err != nil {
		writeError(w, 500, "UPLOAD_STATUS_FAILED", "Unable to load Drive account.")
		return
	}
	uri, err := a.decrypt(encryptedURI)
	if err != nil {
		writeError(w, 500, "UPLOAD_STATUS_FAILED", "Unable to read Google upload session.")
		return
	}
	accessToken, err := a.decrypt(encryptedToken)
	if err != nil {
		writeError(w, 500, "UPLOAD_STATUS_FAILED", "Unable to read Drive token.")
		return
	}
	probe, err := http.NewRequestWithContext(r.Context(), http.MethodPut, uri, nil)
	if err != nil {
		writeError(w, 500, "UPLOAD_STATUS_FAILED", "Unable to create Google upload probe.")
		return
	}
	probe.Header.Set("Authorization", "Bearer "+accessToken)
	probe.Header.Set("Content-Length", "0")
	probe.Header.Set("Content-Range", "bytes */"+fmt.Sprint(size))
	response, err := a.HTTPClient.Do(probe)
	if err != nil {
		writeError(w, 502, "GOOGLE_UNAVAILABLE", "Google upload status request failed.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusPermanentRedirect {
		offset := nextUploadOffset(response.Header.Get("Range"))
		writeJSON(w, http.StatusOK, map[string]string{"status": "uploading", "offset": fmt.Sprint(offset)})
		return
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "completed", "offset": fmt.Sprint(size)})
		return
	}
	writeError(w, 502, "GOOGLE_UPLOAD_STATUS_FAILED", "Google rejected upload status request.")
}

func (a *App) resumableChunk(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	id := r.PathValue("id")
	var accountID, fileName, mimeType, encryptedURI, status string
	var size int64
	err := a.DB.QueryRow(`SELECT target_connected_account_id,file_name,mime_type,size_bytes,google_session_uri,status FROM upload_sessions WHERE id=? AND user_id=?`, id, user.ID).Scan(&accountID, &fileName, &mimeType, &size, &encryptedURI, &status)
	if err == sql.ErrNoRows {
		writeError(w, 404, "UPLOAD_NOT_FOUND", "Upload session not found.")
		return
	}
	if err != nil {
		writeError(w, 500, "UPLOAD_CHUNK_FAILED", "Unable to read upload session.")
		return
	}
	if status == "completed" {
		writeJSON(w, 200, map[string]string{"status": "completed"})
		return
	}
	uri, err := a.decrypt(encryptedURI)
	if err != nil {
		writeError(w, 500, "UPLOAD_CHUNK_FAILED", "Unable to read Google upload session.")
		return
	}
	var encryptedToken string
	err = a.DB.QueryRow(`SELECT access_token_encrypted FROM connected_accounts WHERE id=? AND user_id=?`, accountID, user.ID).Scan(&encryptedToken)
	if err != nil {
		writeError(w, 500, "UPLOAD_CHUNK_FAILED", "Unable to read Drive account.")
		return
	}
	accessToken, err := a.decrypt(encryptedToken)
	if err != nil {
		writeError(w, 500, "UPLOAD_CHUNK_FAILED", "Unable to read Drive token.")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPut, uri, http.MaxBytesReader(w, r.Body, size))
	if err != nil {
		writeError(w, 500, "UPLOAD_CHUNK_FAILED", "Unable to create Google chunk request.")
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Content-Range", r.Header.Get("Content-Range"))
	if length := r.Header.Get("Content-Length"); length != "" {
		req.Header.Set("Content-Length", length)
	}
	response, err := a.HTTPClient.Do(req)
	if err != nil {
		writeError(w, 502, "GOOGLE_UNAVAILABLE", "Google upload chunk failed.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusPermanentRedirect {
		offset := nextUploadOffset(response.Header.Get("Range"))
		writeJSON(w, 200, map[string]string{"status": "uploading", "offset": fmt.Sprint(offset)})
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		writeError(w, 502, "GOOGLE_UPLOAD_FAILED", "Google rejected upload chunk.")
		return
	}
	var uploaded struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		MIMEType string `json:"mimeType"`
		Size     string `json:"size"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&uploaded); err != nil || uploaded.ID == "" {
		writeError(w, 502, "GOOGLE_UPLOAD_FAILED", "Invalid Google upload response.")
		return
	}
	_, err = a.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,provider,provider_file_id,name,mime_type,size_bytes) VALUES (?,?,?,?,?,?,?,?)`, randomID(), user.ID, accountID, "google_drive", uploaded.ID, uploaded.Name, uploaded.MIMEType, size)
	if err != nil {
		writeError(w, 500, "UPLOAD_CHUNK_FAILED", "Unable to save uploaded file.")
		return
	}
	_, _ = a.DB.Exec(`UPDATE upload_sessions SET status='completed',completed_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), id)
	writeJSON(w, 200, map[string]string{"status": "completed"})
}

func nextUploadOffset(rangeValue string) int64 {
	parts := strings.Split(rangeValue, "-")
	if len(parts) != 2 {
		return 0
	}
	var last int64
	if _, err := fmt.Sscan(parts[1], &last); err != nil {
		return 0
	}
	return last + 1
}

func (a *App) selectAccountForUpload(userID string, size int64, target string) (string, error) {
	query := `SELECT c.id FROM connected_accounts c JOIN storage_accounts s ON s.connected_account_id=c.id WHERE c.user_id=? AND c.provider='google_drive' AND c.status='connected' AND s.available_bytes>=?`
	args := []any{userID, size}
	if target != "" {
		query += ` AND c.id=?`
		args = append(args, target)
	}
	query += ` ORDER BY s.available_bytes DESC LIMIT 1`
	var id string
	err := a.DB.QueryRow(query, args...).Scan(&id)
	return id, err
}

func (a *App) selectUploadTarget(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		SizeBytes string `json:"sizeBytes"`
		AccountID string `json:"accountId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, 400, "BAD_REQUEST", "Invalid upload target request.")
		return
	}
	var size int64
	_, err := fmt.Sscan(body.SizeBytes, &size)
	if err != nil || size <= 0 {
		writeError(w, 400, "BAD_REQUEST", "sizeBytes must be a positive integer.")
		return
	}
	query := `SELECT c.id,c.email,COALESCE(s.available_bytes,0) FROM connected_accounts c JOIN storage_accounts s ON s.connected_account_id=c.id WHERE c.user_id=? AND c.provider='google_drive' AND c.status='connected' AND s.available_bytes>=?`
	args := []any{user.ID, size}
	if body.AccountID != "" {
		query += ` AND c.id=?`
		args = append(args, body.AccountID)
	}
	query += ` ORDER BY s.available_bytes DESC LIMIT 1`
	var accountID, email string
	var available int64
	err = a.DB.QueryRow(query, args...).Scan(&accountID, &email, &available)
	if err == sql.ErrNoRows {
		writeError(w, 400, "NO_ACCOUNT_WITH_ENOUGH_SPACE", "No connected Drive account has enough space.")
		return
	}
	if err != nil {
		writeError(w, 500, "UPLOAD_TARGET_FAILED", "Unable to select upload account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"accountId": accountID, "email": email, "availableBytes": fmt.Sprint(available)})
}

func (a *App) downloadFile(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	fileID := r.PathValue("id")
	var providerFileID, name, mimeType, accountID string
	err := a.DB.QueryRow(`SELECT f.provider_file_id,f.name,f.mime_type,c.id FROM files f JOIN connected_accounts c ON c.id=f.connected_account_id WHERE f.id=? AND f.user_id=? AND f.status='active' AND c.provider='google_drive'`, fileID, user.ID).Scan(&providerFileID, &name, &mimeType, &accountID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "FILE_NOT_FOUND", "File not found.")
		return
	}
	if err != nil {
		writeError(w, 500, "DOWNLOAD_FAILED", "Unable to load file.")
		return
	}
	accessToken, err := a.getGoogleToken(r.Context(), accountID, false)
	if err != nil {
		writeError(w, 500, "DOWNLOAD_FAILED", "Unable to read Drive token.")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.GoogleDriveAPIURL+`/files/`+url.PathEscape(providerFileID)+`?alt=media`, nil)
	if err != nil {
		writeError(w, 500, "DOWNLOAD_FAILED", "Unable to create Drive request.")
		return
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	response, err := a.HTTPClient.Do(req)
	
	if err == nil && response.StatusCode == http.StatusUnauthorized {
		if response != nil { response.Body.Close() }
		accessToken, err = a.getGoogleToken(r.Context(), accountID, true)
		if err == nil {
			req, _ = http.NewRequestWithContext(r.Context(), http.MethodGet, a.GoogleDriveAPIURL+`/files/`+url.PathEscape(providerFileID)+`?alt=media`, nil)
			req.Header.Set("Authorization", "Bearer "+accessToken)
			if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
				req.Header.Set("Range", rangeHeader)
			}
			response, err = a.HTTPClient.Do(req)
		}
	}

	if err != nil {
		writeError(w, 502, "GOOGLE_UNAVAILABLE", "Google Drive download failed.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent {
		writeError(w, response.StatusCode, "GOOGLE_DOWNLOAD_FAILED", "Google Drive rejected download.")
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(name, `"`, "'")+`"`)
	for _, header := range []string{"Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (a *App) syncGoogleFiles(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	requestedID := r.URL.Query().Get("connectedAccountId")
	query := `SELECT id FROM connected_accounts WHERE user_id=? AND provider='google_drive' AND status='connected'`
	args := []any{user.ID}
	if requestedID != "" {
		query += ` AND id=?`
		args = append(args, requestedID)
	}
	rows, err := a.DB.Query(query, args...)
	if err != nil {
		writeError(w, 500, "SYNC_FAILED", "Unable to list Drive accounts.")
		return
	}
	defer rows.Close()
	results := make([]map[string]any, 0)
	for rows.Next() {
		var accountID string
		if err := rows.Scan(&accountID); err != nil {
			writeError(w, 500, "SYNC_FAILED", "Unable to read Drive account.")
			return
		}
		accessToken, err := a.getGoogleToken(r.Context(), accountID, false)
		if err != nil {
			results = append(results, map[string]any{"accountId": accountID, "error": "Unable to read Drive token"})
			continue
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.GoogleDriveAPIURL+`/files?pageSize=1000&orderBy=modifiedTime%20desc&fields=files(id,name,mimeType,size,createdTime,modifiedTime,trashed)`, nil)
		if err != nil {
			results = append(results, map[string]any{"accountId": accountID, "error": "Unable to create Drive request"})
			continue
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		response, err := a.HTTPClient.Do(req)
		if err != nil || response.StatusCode != http.StatusOK {
			if response != nil {
				response.Body.Close()
			}
			results = append(results, map[string]any{"accountId": accountID, "error": "Google Drive listing failed"})
			continue
		}
		var payload struct {
			Files []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				MIMEType string `json:"mimeType"`
				Size     string `json:"size"`
				Created  string `json:"createdTime"`
				Modified string `json:"modifiedTime"`
				Trashed  bool   `json:"trashed"`
			} `json:"files"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&payload)
		response.Body.Close()
		if err != nil {
			results = append(results, map[string]any{"accountId": accountID, "error": "Invalid Google Drive response"})
			continue
		}
		created, updated := 0, 0
		for _, item := range payload.Files {
			if item.ID == "" || item.Trashed {
				continue
			}
			var size int64
			_, _ = fmt.Sscan(item.Size, &size)
			var exists int
			_ = a.DB.QueryRow(`SELECT 1 FROM files WHERE user_id=? AND connected_account_id=? AND provider_file_id=?`, user.ID, accountID, item.ID).Scan(&exists)
			if exists == 1 {
				_, err = a.DB.Exec(`UPDATE files SET name=?,mime_type=?,size_bytes=?,updated_at=? WHERE user_id=? AND connected_account_id=? AND provider_file_id=?`, item.Name, item.MIMEType, size, item.Modified, user.ID, accountID, item.ID)
				if err == nil {
					updated++
				}
			} else {
				_, err = a.DB.Exec(`INSERT INTO files (id,user_id,connected_account_id,provider,provider_file_id,name,mime_type,size_bytes,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, randomID(), user.ID, accountID, "google_drive", item.ID, item.Name, item.MIMEType, size, item.Created, item.Modified)
				if err == nil {
					created++
				}
			}
		}
		results = append(results, map[string]any{"accountId": accountID, "created": created, "updated": updated})
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "results": results})
}

func parseScopes(raw string) []string {
	var scopes []string
	_ = json.Unmarshal([]byte(raw), &scopes)
	return scopes
}

func (a *App) googleCallback(w http.ResponseWriter, r *http.Request) {
	wantsJSON := r.Header.Get("Accept") == "application/json" || r.Header.Get("Content-Type") == "application/json" || r.Header.Get("Authorization") != ""
	
	redirectError := func() {
		if wantsJSON {
			writeError(w, http.StatusBadRequest, "OAUTH_FAILED", "OAuth connection failed")
		} else {
			http.Redirect(w, r, a.Config.FrontendURL+"/google-connected?status=error", http.StatusFound)
		}
	}
	query := r.URL.Query()
	state, code := query.Get("state"), query.Get("code")
	if state == "" || code == "" || query.Get("error") != "" {
		redirectError()
		return
	}

	var stateID, userID, configID, expiresAt, encryptedID, encryptedSecret, redirectURI, scopes string
	err := a.DB.QueryRow(`SELECT s.id,s.user_id,s.provider_config_id,s.expires_at,p.client_id_encrypted,p.client_secret_encrypted,p.redirect_uri,p.scopes FROM oauth_states s JOIN provider_configs p ON p.id=s.provider_config_id WHERE s.state_hash=? AND s.flow='connect' AND s.used_at IS NULL`, hashToken(state)).Scan(&stateID, &userID, &configID, &expiresAt, &encryptedID, &encryptedSecret, &redirectURI, &scopes)
	if err != nil {
		redirectError()
		return
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(time.Now()) {
		redirectError()
		return
	}
	clientID, err := a.decrypt(encryptedID)
	if err != nil {
		redirectError()
		return
	}
	clientSecret, err := a.decrypt(encryptedSecret)
	if err != nil {
		redirectError()
		return
	}

	conf := &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURI, Endpoint: a.GoogleEndpoint, Scopes: parseScopes(scopes)}
	token, err := conf.Exchange(r.Context(), code)
	if err != nil || token.AccessToken == "" {
		log.Printf("Google OAuth token exchange failed: %v", err)
		redirectError()
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, a.GoogleUserInfoURL, nil)
	if err != nil {
		redirectError()
		return
	}
	request.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err := a.HTTPClient.Do(request)
	if err != nil {
		log.Printf("Google userinfo request failed: %v", err)
		redirectError()
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		log.Printf("Google userinfo status: %d", response.StatusCode)
		redirectError()
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		log.Printf("Google userinfo read failed: %v", err)
		redirectError()
		return
	}
	var profile struct {
		ID      string `json:"id"`
		Email   string `json:"email"`
		Name    string `json:"name"`
		Picture string `json:"picture"`
	}
	if err := json.Unmarshal(body, &profile); err != nil || profile.ID == "" || profile.Email == "" {
		log.Printf("Google userinfo payload invalid: %v", err)
		redirectError()
		return
	}
	refreshToken := token.RefreshToken
	if refreshToken == "" {
		_ = a.DB.QueryRow(`SELECT refresh_token_encrypted FROM connected_accounts WHERE user_id=? AND provider='google_drive' AND provider_account_id=?`, userID, profile.ID).Scan(&refreshToken)
		if refreshToken != "" {
			refreshToken, _ = a.decrypt(refreshToken)
		}
	}
	if refreshToken == "" {
		redirectError()
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	expiresToken := token.Expiry.UTC().Format(time.RFC3339Nano)
	_, err = a.DB.Exec(`INSERT INTO connected_accounts (id,user_id,provider_config_id,provider,provider_account_id,email,display_name,avatar_url,access_token_encrypted,refresh_token_encrypted,token_expires_at,scopes,status,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'connected',?) ON CONFLICT(user_id,provider,provider_account_id) DO UPDATE SET provider_config_id=excluded.provider_config_id,email=excluded.email,display_name=excluded.display_name,avatar_url=excluded.avatar_url,access_token_encrypted=excluded.access_token_encrypted,refresh_token_encrypted=excluded.refresh_token_encrypted,token_expires_at=excluded.token_expires_at,scopes=excluded.scopes,status='connected',updated_at=excluded.updated_at`, randomID(), userID, configID, "google_drive", profile.ID, profile.Email, profile.Name, profile.Picture, a.encrypt(token.AccessToken), a.encrypt(refreshToken), expiresToken, scopes, now)
	if err != nil {
		log.Printf("Google account persistence failed: %v", err)
		redirectError()
		return
	}
	_, _ = a.DB.Exec(`UPDATE oauth_states SET used_at=? WHERE id=?`, now, stateID)
	if wantsJSON {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	} else {
		http.Redirect(w, r, a.Config.FrontendURL+"/google-connected?status=success", http.StatusFound)
	}
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, r.Context().Value(userKey).(authUser))
}

func (a *App) updateMe(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Name == "" || body.Email == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Name and email are required.")
		return
	}
	if body.Password != "" {
		hash, _ := bcrypt.GenerateFromPassword([]byte(body.Password), 10)
		_, err := a.DB.Exec(`UPDATE users SET name=?, email=?, password_hash=? WHERE id=?`, body.Name, body.Email, hash, user.ID)
		if err != nil {
			writeError(w, http.StatusConflict, "EMAIL_IN_USE", "Email already in use.")
			return
		}
	} else {
		_, err := a.DB.Exec(`UPDATE users SET name=?, email=? WHERE id=?`, body.Name, body.Email, user.ID)
		if err != nil {
			writeError(w, http.StatusConflict, "EMAIL_IN_USE", "Email already in use.")
			return
		}
	}
	user.Name = body.Name
	user.Email = body.Email
	a.respondSession(w, http.StatusOK, user)
}

func (a *App) signAccessToken(user authUser) (string, error) {
	claims := jwt.MapClaims{"sub": user.ID, "name": user.Name, "email": user.Email, "exp": time.Now().Add(15 * time.Minute).Unix()}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(a.Config.JWTSecret))
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenString := ""
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) == 2 && parts[0] == "Bearer" {
			tokenString = parts[1]
		} else if qToken := r.URL.Query().Get("token"); qToken != "" {
			tokenString = qToken
		}

		if tokenString == "" {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication required.")
			return
		}
		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (any, error) {
			if t.Method != jwt.SigningMethodHS256 {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(a.Config.JWTSecret), nil
		})
		if err != nil || !token.Valid {
			writeError(w, http.StatusUnauthorized, "AUTH_INVALID", "Invalid or expired session.")
			return
		}
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			writeError(w, http.StatusUnauthorized, "AUTH_INVALID", "Invalid session.")
			return
		}
		id, _ := claims.GetSubject()
		name, _ := claims["name"].(string)
		email, _ := claims["email"].(string)
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, authUser{ID: id, Name: name, Email: email})))
	}
}

func (a *App) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", a.Config.FrontendURL)
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func jsonNotFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "NOT_FOUND", "Route not found")
}
func decodeJSON(r *http.Request, value any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(value)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
func randomID() string { return fmt.Sprintf("%d-%d", time.Now().UnixNano(), os.Getpid()) }

func randomToken() string {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes)
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (a *App) encryptionKey() []byte {
	sum := sha256.Sum256([]byte(a.Config.TokenKey))
	return sum[:]
}

func (a *App) encrypt(value string) string {
	block, err := aes.NewCipher(a.encryptionKey())
	if err != nil {
		panic(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return hex.EncodeToString(append(nonce, gcm.Seal(nil, nonce, []byte(value), nil)...))
}

func (a *App) decrypt(value string) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(a.encryptionKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted value")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	return string(plain), err
}

func main() {
	config := loadConfig()
	db, err := sql.Open("sqlite", config.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	app := &App{DB: db, Config: config, HTTPClient: http.DefaultClient, GoogleEndpoint: google.Endpoint, GoogleUserInfoURL: "https://www.googleapis.com/oauth2/v2/userinfo", GoogleDriveAPIURL: "https://www.googleapis.com/drive/v3", GoogleUploadAPIURL: "https://www.googleapis.com/upload/drive/v3/files"}
	if err := app.migrate(); err != nil {
		log.Fatal(err)
	}
	if err := app.ensureInitialAdmin(); err != nil {
		log.Fatal(err)
	}
	log.Printf("9Drive Go listening on http://127.0.0.1:%s", config.AppPort)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+config.AppPort, app.Router()))
}

func (a *App) viewFileUrl(w http.ResponseWriter, r *http.Request) {
	// For now, return empty URL so frontend falls back to stream preview
	writeJSON(w, http.StatusOK, map[string]string{"url": ""})
}

func (a *App) shareFileUrl(w http.ResponseWriter, r *http.Request) {
	// Future: generate signed URL or public link
	writeJSON(w, http.StatusOK, map[string]string{"url": a.Config.FrontendURL + "/files/" + r.PathValue("id")})
}

func (a *App) publicPermission(w http.ResponseWriter, r *http.Request) {
	// Future: Google Drive API permissions insert
	writeJSON(w, http.StatusOK, map[string]string{"url": "https://drive.google.com/open?id=not_implemented_yet"})
}

func (a *App) updateFile(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	fileID := r.PathValue("id")
	var body struct {
		Name     *string `json:"name"`
		FolderID *string `json:"folderId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid body")
		return
	}
	if body.Name != nil {
		_, _ = a.DB.Exec(`UPDATE files SET name=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND status='active'`, *body.Name, fileID, user.ID)
	}
	if body.FolderID != nil {
		if *body.FolderID == "" {
			_, _ = a.DB.Exec(`UPDATE files SET folder_id=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND status='active'`, fileID, user.ID)
		} else {
			_, _ = a.DB.Exec(`UPDATE files SET folder_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND status='active'`, *body.FolderID, fileID, user.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) deleteFile(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	fileID := r.PathValue("id")
	_, _ = a.DB.Exec(`UPDATE files SET status='deleted', deleted_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`, fileID, user.ID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) batchUpdateFiles(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		FileIDs  []string `json:"fileIds"`
		FolderID *string  `json:"folderId"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.FileIDs) == 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid body")
		return
	}
	if body.FolderID != nil {
		for _, id := range body.FileIDs {
			if *body.FolderID == "" {
				_, _ = a.DB.Exec(`UPDATE files SET folder_id=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND status='active'`, id, user.ID)
			} else {
				_, _ = a.DB.Exec(`UPDATE files SET folder_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND status='active'`, *body.FolderID, id, user.ID)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) batchDeleteFiles(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var body struct {
		FileIDs []string `json:"fileIds"`
	}
	if err := decodeJSON(r, &body); err != nil || len(body.FileIDs) == 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid body")
		return
	}
	for _, id := range body.FileIDs {
		_, _ = a.DB.Exec(`UPDATE files SET status='deleted', deleted_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`, id, user.ID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) updateFolder(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	folderID := r.PathValue("id")
	var body struct {
		Name     *string `json:"name"`
		Color    *string `json:"color"`
		IconUrl  *string `json:"iconUrl"`
		ParentID *string `json:"parentId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid body")
		return
	}
	if body.Name != nil {
		_, _ = a.DB.Exec(`UPDATE folders SET name=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND deleted_at IS NULL`, *body.Name, folderID, user.ID)
	}
	if body.Color != nil {
		_, _ = a.DB.Exec(`UPDATE folders SET color=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND deleted_at IS NULL`, *body.Color, folderID, user.ID)
	}
	if body.ParentID != nil {
		if *body.ParentID == "" {
			_, _ = a.DB.Exec(`UPDATE folders SET parent_id=NULL, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND deleted_at IS NULL`, folderID, user.ID)
		} else {
			_, _ = a.DB.Exec(`UPDATE folders SET parent_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=? AND deleted_at IS NULL`, *body.ParentID, folderID, user.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) deleteFolder(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	folderID := r.PathValue("id")
	_, _ = a.DB.Exec(`UPDATE folders SET deleted_at=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP WHERE id=? AND user_id=?`, folderID, user.ID)
	// Optionally mark all files inside as deleted as well, or leave them unreachable via list but physically there.
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) batchDownloadZip(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userKey).(authUser)
	var fileIDs []string
	
	if r.Header.Get("Content-Type") == "application/json" {
		var body struct { FileIDs []string `json:"fileIds"` }
		if err := decodeJSON(r, &body); err == nil {
			fileIDs = body.FileIDs
		}
	} else {
		// Form POST
		val := r.FormValue("fileIds")
		_ = json.Unmarshal([]byte(val), &fileIDs)
	}

	if len(fileIDs) == 0 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Invalid fileIds")
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="9drive-download.zip"`)
	
	zw := zip.NewWriter(w)
	var errorLog strings.Builder

	for _, fileID := range fileIDs {
		var provider, providerFileID, accID, fileName, mimeType string
		err := a.DB.QueryRow(`SELECT provider, provider_file_id, connected_account_id, name, mime_type FROM files WHERE id=? AND user_id=? AND status='active'`, fileID, user.ID).Scan(&provider, &providerFileID, &accID, &fileName, &mimeType)
		if err != nil {
			errorLog.WriteString(fmt.Sprintf("File ID %s: Not found in local database\n", fileID))
			continue
		}
		
		if strings.HasPrefix(mimeType, "application/vnd.google-apps.") {
			errorLog.WriteString(fmt.Sprintf("File %s: Google native document formats (docs, sheets, forms) cannot be downloaded directly via ZIP.\n", fileName))
			continue
		}

		if provider == "google_drive" {
			accessToken, err := a.getGoogleToken(r.Context(), accID, false)
			if err != nil {
				errorLog.WriteString(fmt.Sprintf("File %s: Account token missing or expired (%v)\n", fileName, err))
				continue
			}
			url := fmt.Sprintf("%s/files/%s?alt=media", a.GoogleDriveAPIURL, providerFileID)
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
			req.Header.Set("Authorization", "Bearer "+accessToken)
			resp, err := a.HTTPClient.Do(req)
			if err == nil && resp.StatusCode == http.StatusUnauthorized {
				if resp != nil { resp.Body.Close() }
				accessToken, err = a.getGoogleToken(r.Context(), accID, true)
				if err == nil {
					req, _ = http.NewRequestWithContext(r.Context(), http.MethodGet, url, nil)
					req.Header.Set("Authorization", "Bearer "+accessToken)
					resp, err = a.HTTPClient.Do(req)
				}
			}
			if err != nil {
				errorLog.WriteString(fmt.Sprintf("File %s: Google API request failed (%v)\n", fileName, err))
				continue
			}
			if resp.StatusCode != http.StatusOK {
				bodyBytes, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				errorLog.WriteString(fmt.Sprintf("File %s: Google API error %d - %s\n", fileName, resp.StatusCode, string(bodyBytes)))
				continue
			}
			
			f, err := zw.Create(fileName)
			if err == nil {
				io.Copy(f, resp.Body)
			}
			resp.Body.Close()
		}
	}

	if errorLog.Len() > 0 {
		f, _ := zw.Create("9drive-errors.txt")
		io.WriteString(f, errorLog.String())
	}
	zw.Close()
}
