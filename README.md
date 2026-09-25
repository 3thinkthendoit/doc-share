# <img src="web/static/logo-mark.svg" height="36" alt="DocShare logo"/> DocShare

[中文说明](README.zh-CN.md) | English

A lightweight self-hosted documentation sharing platform built with Go and Gin. Write documents in Markdown, organize them with projects and categories, and share them via tokenized links — with password protection, access requests, comments & revisions, plus Open API / MCP integration.

## Features

### Documents

- **Markdown editor** — toolbar (bold/lists/code/link/image/table, etc.), live preview, XSS-sanitized rendering
- **Image upload** — paste or drag-and-drop; storage is either local disk (`/uploads`) or RustFS (S3-compatible)
- **Import** — convert `PDF` / `doc` / `docx` to Markdown in memory (no temp files on disk)
- **List & filters** — search by title; filter by project, category, share status, ownership (mine / project-shared); pagination
- **Preview** — admin reader preview without incrementing view counts
- **Edit presence** — heartbeat warns when someone else is editing the same document

### Sharing & access

- **Share links** — public `/s/:token`; revoke anytime
- **Password** — optional; generate / reset; copy title + link + password together
- **Expiry** — permanent / 1 / 7 / 30 days
- **Request access** — on password-protected shares, guests or signed-in users can apply with a display name; owners approve from share settings or the dashboard; approved viewers skip the password (site-wide toggle in admin settings)
- **Allow edit** — optionally let signed-in users edit the document on the share page
- **View counts** — tracked on the reader page

### Collaboration

- **Comments** — on share pages for signed-in users and guests (rate-limited); owners/admins can delete
- **Revisions** — automatic snapshots before share-page overwrites; list and roll back from the editor
- **Approved request = unlocked** — same as entering the correct password: read and comment; save content if “allow edit” is on

### Projects & categories

- **Projects** — personal CRUD; attach documents; filter by name / owner (admins)
- **Members** — invite users with **view** / **edit** roles; members see project docs; can leave; only the owner manages members
- **Categories** — personal CRUD and sort order; deleting a category leaves documents uncategorized

### Accounts & permissions

- **Register / login** — captcha; registration mode is admin-configurable: username / email (SMTP verification code) / phone (format check only)
- **Profile** — nickname, email, phone, avatar; change own password
- **Roles** — `admin` (site-wide) / `viewer` (own workspace); enable/disable users
- **User admin** — create, edit, delete, reset password, change role and status

### Dashboard & console

- **Dashboard** — counts for documents, shares, users (admin), linked projects; recent documents
- **Pending access requests** — badge + modal list to approve/reject
- **System settings** (admin) — site name, logo, public domain; allow access requests; registration method; SMTP (with test mail); storage local / RustFS (with connectivity test)
- **i18n** — English, 简体中文, 繁體中文, 日本語, Français

### Integrations

- **API keys** — create personal keys; enable/disable, reset secret (shown once), delete
- **Open API** — `/openapi/v1` with HMAC auth (AppKey + timestamp + nonce + signature); full CRUD for docs/projects/categories as the key owner
- **In-app API docs** — `/admin/apidoc` for signing rules and endpoints
- **MCP server** — zero-dependency Node stdio server for CodeBuddy / Claude / Cursor and similar clients

## Tech Stack

- Go 1.26, [Gin](https://github.com/gin-gonic/gin)
- MySQL via [GORM](https://gorm.io)
- Server-rendered HTML templates + vanilla JS/CSS (no frontend build step)
- Templates, static assets, and locale files embedded with `go:embed` (single binary)
- Session HMAC signing, bcrypt passwords, signed share credentials

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

Site name, logo, registration mode, SMTP, RustFS, and similar options are managed in **System Settings** (`/admin/settings`) after login — no config file edits required.

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
  storage/              local / RustFS storage
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

## Main Pages & APIs

| Area | Paths |
|---|---|
| Landing | `GET /` |
| Auth | `GET/POST /login`, `GET/POST /register`, `POST /register/email-code`, `GET /captcha`, `GET/POST /logout` |
| Admin pages | `/admin`, `/admin/docs`, `/admin/projects`, `/admin/categories`, `/admin/apikeys`, `/admin/apidoc`, `/admin/users`, `/admin/settings` |
| Document APIs | `POST/PUT/DELETE /admin/api/docs`, share `POST/DELETE /admin/api/docs/:id/share` |
| Access requests | `POST /s/:token/access-request`; list/review `/admin/api/access-requests`, `/admin/api/docs/:id/access-requests` |
| Share | `GET/POST /s/:token`; comments `GET/POST /s/:token/comments`; save `PUT /s/:token/content` |
| Open API | `/openapi/v1/docs`, `/projects`, `/categories` (full CRUD, HMAC) |
| Upload / Convert | `POST /admin/api/upload`, `POST /admin/api/convert` |

Full signing rules and error codes: in-app **API docs** at `/admin/apidoc`.

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
