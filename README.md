# <img src="web/static/logo-mark.svg" height="36" alt="DocShare logo"/> DocShare

[中文说明](README.zh-CN.md) | English

A lightweight self-hosted documentation sharing platform built with Go and Gin. Write documents in Markdown, manage them with projects and categories, and share them publicly via tokenized links — with optional password protection.

## Features

- **Markdown documents** — create, edit and organize documents with a built-in editor and image uploads (stored locally, served at `/uploads`)
- **Document import** — convert `PDF` / `doc` / `docx` files to Markdown in memory, no disk temp files
- **Sharing links** — publish any document via `/s/:token`, optionally protected by a password
- **Admin dashboard** — manage documents, projects, categories, users and API keys
- **Open API** — full CRUD for documents/projects/categories authenticated by API keys (HMAC)
- **MCP server** — built-in zero-dependency MCP server so AI clients (CodeBuddy / Claude / Cursor) can manage documents directly
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
mcp/
  mcp-server.js         zero-dependency MCP server (Node, stdio) wrapping the Open API
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

## MCP Server (AI Client Integration)

`mcp/mcp-server.js` is a zero-dependency MCP server (Node >= 18, stdio transport) that wraps the Open API into MCP tools, so AI clients such as CodeBuddy / Claude Desktop / Cursor can manage documents, projects and categories directly.

### 1. Create an API Key

Create a key at `/admin/apikeys` in the DocShare admin panel. You'll get an `AppKey` (prefix `ak_`) and a `Secret` (prefix `sk_`, shown only once). The key owner owns the resulting documents; permissions match the web UI.

### 2. Client Configuration

Add the server to your MCP client's `mcpServers` config:

```json
{
  "mcpServers": {
    "docshare": {
      "command": "node",
      "args": ["/path/to/doc-share/mcp/mcp-server.js"],
      "env": {
        "DOC_SHARE_BASE_URL": "http://127.0.0.1:9000",
        "DOC_SHARE_APP_KEY": "ak_xxx",
        "DOC_SHARE_SECRET": "sk_xxx"
      }
    }
  }
}
```

Alternatively, skip the env vars and create `mcp/mcp.config.json` (see `mcp/mcp.config.example.json`; git-ignored):

```json
{
  "baseUrl": "http://127.0.0.1:9000",
  "appKey": "ak_xxx",
  "secret": "sk_xxx"
}
```

Environment variables take precedence over the config file; `DOC_SHARE_BASE_URL` defaults to `http://127.0.0.1:9000`.

### 3. Available Tools

| Tool | Description |
|---|---|
| `docshare_list_docs` | Paged document list (without content), filterable by `project_id` / `category_id` |
| `docshare_get_doc` | Read a single document with full Markdown content |
| `docshare_create_doc` / `docshare_update_doc` / `docshare_delete_doc` | Document CRUD (`content` is fully replaced on update; leave `title` empty to keep it) |
| `docshare_list_projects` / `docshare_create_project` / `docshare_update_project` / `docshare_delete_project` | Project management (documents become ungrouped after project deletion) |
| `docshare_list_categories` / `docshare_create_category` / `docshare_update_category` / `docshare_delete_category` | Category management (documents become uncategorized after category deletion) |

## License

See [LICENSE](LICENSE).
