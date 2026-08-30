# 9Drive Lite

Google Drive multi-account gateway. Native Go backend, SQLite database, React frontend.

## Status

Migration from TypeScript/Prisma to Go is complete. All API routes, OAuth flow, multiple account persistence, file metadata sync, resumable upload routing, and range download streams have been implemented in Go. The legacy TypeScript backend has been removed.

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

## Go backend features

Implemented and tested:

- `GET /health`
- `POST /auth/register` (Bootstrap only, disabled afterwards)
- `POST /auth/login`
- `POST /auth/refresh`
- `POST /auth/logout`
- `GET /auth/me`
- `PUT /auth/me`
- `GET/POST /system/google-config`
- `GET /connected-accounts/google/connect-url`
- `GET /connected-accounts/google/callback`
- `GET /connected-accounts`
- `GET /storage/summary`
- `POST /sync/quota`
- `POST /sync/files`
- `GET /files`
- `GET /folders`
- `POST /folders`
- `GET /files/{id}/download`
- `POST /upload/resumable`
- `PUT /upload/resumable/{id}`
- `GET /upload/resumable/{id}`
- JWT access tokens, hashed refresh tokens, SQLite sessions
- AES-GCM encrypted Google OAuth credentials
- OAuth state hashing and 10-minute expiry
- Resumable uploads directly to Google Drive chunks
- Range-based byte stream downloads

Build:

```bash
cd backend-go
go test ./...
go build -o ../bin/9drive .
```

## Run local Go backend

```powershell
cd backend-go
$env:APP_PORT="4000"
$env:FRONTEND_URL="http://localhost:5173"
$env:JWT_ACCESS_SECRET="replace-with-strong-random-secret"
$env:TOKEN_ENCRYPTION_KEY="replace-with-another-strong-random-secret"
$env:DATABASE_URL="file:data/9drive.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
go run .
```

## Google OAuth setup

1. Create Google OAuth Web Application credential.
2. Enable Google Drive API.
3. Set authorized redirect URI:

```text
http://localhost:4000/connected-accounts/google/callback
```

4. Register/login to 9Drive.
5. Save Client ID and Client Secret through Settings UI.

## Security

- Do not commit `.env` or SQLite DB files.
- Keep backend bound to `127.0.0.1` behind Nginx on VPS.
- Use HTTPS before exposing outside localhost.
- Google Client Secret, OAuth tokens, and token encryption key are secrets.

## License

MIT
