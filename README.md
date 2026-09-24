# DocShare

[中文说明](README.zh-CN.md) | English

A lightweight self-hosted documentation sharing platform built with Go and Gin. Write documents in Markdown, manage them with projects and categories, and share them publicly via tokenized links — with optional password protection.

## Features

- **Markdown documents** — create, edit and organize documents with a built-in editor and image uploads (stored locally, served at `/uploads`)
- **Document import** — convert `PDF` / `doc` / `docx` files to Markdown in memory, no disk temp files
- **Sharing links** — publish any document via `/s/:token`, optionally protected by a password
- **Admin dashboard** — manage documents, projects, categories, users and API keys
- **Open API** — full CRUD for documents/projects/categories authenticated by API keys (HMAC)
- **Multi-language UI** — English, 简体中文, 繁體中文, 日本語, Français
- **Security** — session signing (HMAC), captcha on login/register, bcrypt password hashing
- **Single binary** — templates, static assets and i18n dictionaries are embedded via `go:embed`

## Tech Stack

- Go 1.26, [Gin](https://github.com/gin-gonic/gin)
- MySQL via [GORM](https://gorm.io)
- Server-rendered HTML templates + vanilla JS/CSS (no frontend build step)

## Quick Start

### 1. Requirements

- Go 1.26+
- MySQL 8.x (create an empty database first):

```sql
CREATE DATABASE doc_share DEFAULT CHARSET utf8mb4;
```

### 2. Configure

All settings live in `config.yaml` and can be overridden via environment variables. Placeholders support `${VAR}` and `${VAR:-default}` syntax.

| Variable | Required | Description |
|---|---|---|
| `DOC_SHARE_SESSION_SECRET` | Yes | HMAC key for sessions & share tokens (≥ 32 random chars). Server refuses to start without it. |
| `DOC_SHARE_DB_DSN` | No | MySQL DSN (default: `root:root@tcp(127.0.0.1:3306)/doc_share?charset=utf8mb4&parseTime=True&loc=Local`) |
| `DOC_SHARE_PORT` | No | Listen address (default `:8080`) |
| `DOC_SHARE_ADMIN_USER` | No | Initial admin username (default `admin`) |
| `DOC_SHARE_ADMIN_PASSWORD` | No | Initial admin password (default `admin123`) |
| `DOC_SHARE_UPLOAD_DIR` | No | Image upload directory (default `./uploads`) |
| `DOC_SHARE_UPLOAD_MAX_MB` | No | Max image size in MB (default `10`) |
| `DOC_SHARE_DEV` | No | `true` = serve templates/statics from disk for hot reload |
| `DOC_SHARE_DB_LOG` | No | `true` = enable GORM SQL logging |

> For local debugging only, `DOC_SHARE_ALLOW_INSECURE_DEFAULTS=true` temporarily bypasses the mandatory session secret.

### 3. Run

```bash
export DOC_SHARE_SESSION_SECRET="change-me-to-a-random-string-32chars"
go run .
# or
go build -o doc-share .
./doc-share -config config.yaml
```

Visit `http://localhost:8080` and log in with the default admin account (change the password immediately).

## Project Layout

```
main.go                 entry point, embedded assets (go:embed)
config.yaml             configuration with env placeholder support
internal/
  config/               YAML loading + ${VAR} expansion
  database/             GORM/MySQL init and auto-migration
  handler/              HTTP handlers (admin, open API, share, auth, upload)
  middleware/           auth / API-key / i18n middleware
  model/                GORM models
  router/               route registration
  session/              HMAC session signer
  i18n/                 locale bundle loader
  convert/              PDF / doc / docx → Markdown converters
  util/                 helpers
web/
  templates/            HTML templates
  static/               CSS / JS
  locales/              en-US, zh-CN, zh-TW, ja-JP, fr-FR
```

## Development Mode

Set `DOC_SHARE_DEV=true` to read templates and static files directly from the `web/` directory — front-end changes take effect on browser refresh without recompiling or restarting. Keep it `false` in production (embedded assets).

## API Overview

| Area | Endpoints |
|---|---|
| Auth | `GET/POST /login`, `GET/POST /register`, `GET /captcha`, `GET /logout` |
| Admin pages | `/admin`, `/admin/docs`, `/admin/projects`, `/admin/categories`, `/admin/apikeys`, `/admin/users` |
| Documents | `POST/PUT/DELETE /api/docs`, `POST /api/docs/:id/share` |
| Share view | `GET/POST /s/:token` |
| Open API (API key) | `/open/docs`, `/open/projects`, `/open/categories` (full CRUD) |
| Upload / Convert | `POST /api/upload`, `POST /api/convert` |

## License

See [LICENSE](LICENSE).
