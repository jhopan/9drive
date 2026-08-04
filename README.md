# 9Drive Lite

Google Drive multi-account gateway. Native Go backend, SQLite database, React frontend.

## Status

In migration from TypeScript/Prisma to Go. Go auth and initial OAuth URL flow work. Google OAuth callback, Drive account persistence, quota, files, folders, upload, and download are pending.

## Runtime target

```text
VPS: 1 vCPU / 1 GB RAM
Database: SQLite WAL
Backend: one Go binary
Frontend: static Vite build
No Docker
No MySQL
No S3
```

## Current Go backend

Implemented and tested:

- `GET /health`
- `POST /auth/register`
- `POST /auth/login`
- `POST /auth/refresh`
- `POST /auth/logout`
- `GET /auth/me`
- `GET/POST /system/google-config`
- `GET /connected-accounts/google/connect-url`
- JWT access tokens, hashed refresh tokens, SQLite sessions
- AES-GCM encrypted Google OAuth credentials
- OAuth state hashing and 10-minute expiry

Build:

```bash
cd backend-go
go test ./...
go build -o ../bin/9drive .
```

## Run local Go backend

```bash
cd backend-go
set APP_PORT=4000
set FRONTEND_URL=http://localhost:5173
set JWT_ACCESS_SECRET=replace-with-strong-random-secret
set TOKEN_ENCRYPTION_KEY=replace-with-another-strong-random-secret
set DATABASE_URL=file:data/9drive.db?_pragma=journal_mode(WAL)^&_pragma=busy_timeout(5000)
go run .
```

Windows PowerShell uses `$env:NAME='value'` instead of `set`.

## Google OAuth setup

1. Create Google OAuth Web Application credential.
2. Enable Google Drive API.
3. Set authorized redirect URI:

```text
http://localhost:4000/connected-accounts/google/callback
```

4. Register/login to 9Drive.
5. Save Client ID and Client Secret through Google config endpoint or Settings UI after Go callback/UI parity lands.

## Next work

- [ ] Implement `GET /connected-accounts/google/callback` in Go.
- [ ] Exchange authorization code for Google access and refresh tokens.
- [ ] Save/update multiple Google Drive accounts encrypted in SQLite.
- [ ] Sync per-account quota.
- [ ] Port folder/file metadata routes.
- [ ] Port Google resumable upload routing.
- [ ] Port native range download and preview stream.
- [ ] Point React frontend fully to Go API and run full local regression tests.
- [ ] Remove legacy TypeScript backend only after parity verification.

## Security

- Do not commit `.env` or SQLite DB files.
- Keep backend bound to `127.0.0.1` behind Nginx on VPS.
- Use HTTPS before exposing outside localhost.
- Google Client Secret, OAuth tokens, and token encryption key are secrets.

## License

MIT
