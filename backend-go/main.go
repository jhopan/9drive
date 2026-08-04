package main

import (
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
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
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
	DB     *sql.DB
	Config Config
}

type authUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type ctxKey string

const userKey ctxKey = "user"

func loadConfig() Config {
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

func (a *App) Router() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("POST /auth/register", a.register)
	mux.HandleFunc("POST /auth/login", a.login)
	mux.HandleFunc("POST /auth/refresh", a.refresh)
	mux.HandleFunc("POST /auth/logout", a.requireAuth(a.logout))
	mux.HandleFunc("GET /auth/me", a.requireAuth(a.me))
	mux.HandleFunc("GET /system/google-config", a.requireAuth(a.getGoogleConfig))
	mux.HandleFunc("POST /system/google-config", a.requireAuth(a.saveGoogleConfig))
	mux.HandleFunc("GET /connected-accounts/google/connect-url", a.requireAuth(a.googleConnectURL))
	mux.HandleFunc("/", jsonNotFound)
	return a.cors(mux)
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var body struct{ Name, Email, Password string }
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	body.Name, body.Email = strings.TrimSpace(body.Name), strings.ToLower(strings.TrimSpace(body.Email))
	if body.Name == "" || !strings.Contains(body.Email, "@") || len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "Name, valid email, and password of at least 8 characters are required.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PASSWORD_HASH_FAILED", "Unable to secure password.")
		return
	}
	user := authUser{ID: randomID(), Name: body.Name, Email: body.Email}
	_, err = a.DB.Exec(`INSERT INTO users (id,name,email,password_hash) VALUES (?,?,?,?)`, user.ID, user.Name, user.Email, string(hash))
	if err != nil {
		writeError(w, http.StatusConflict, "EMAIL_EXISTS", "Email already registered.")
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

func parseScopes(raw string) []string {
	var scopes []string
	_ = json.Unmarshal([]byte(raw), &scopes)
	return scopes
}

func (a *App) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, r.Context().Value(userKey).(authUser))
}

func (a *App) signAccessToken(user authUser) (string, error) {
	claims := jwt.MapClaims{"sub": user.ID, "name": user.Name, "email": user.Email, "exp": time.Now().Add(15 * time.Minute).Unix()}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(a.Config.JWTSecret))
}

func (a *App) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || parts[0] != "Bearer" {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication required.")
			return
		}
		token, err := jwt.Parse(parts[1], func(t *jwt.Token) (any, error) {
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
	app := &App{DB: db, Config: config}
	if err := app.migrate(); err != nil {
		log.Fatal(err)
	}
	log.Printf("9Drive Go listening on http://127.0.0.1:%s", config.AppPort)
	log.Fatal(http.ListenAndServe("127.0.0.1:"+config.AppPort, app.Router()))
}
