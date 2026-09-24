# DocShare

中文说明 | [English](README.md)

基于 Go + Gin 的轻量级自托管文档分享平台。用 Markdown 编写文档，按项目/分类组织管理，通过带令牌的链接对外分享——支持可选的访问密码。

## 功能特性

- **Markdown 文档** — 内置编辑器创建、编辑文档，支持图片上传（本地存储，`/uploads` 路径访问）
- **文档导入** — `PDF` / `doc` / `docx` 文件纯内存转换为 Markdown，不落盘
- **分享链接** — 任意文档可通过 `/s/:token` 公开访问，支持密码保护
- **管理后台** — 管理文档、项目、分类、用户和 API 密钥
- **开放 API** — 通过 API Key（HMAC 签名）认证，支持文档/项目/分类完整增删改查
- **多语言界面** — 简体中文、繁體中文、English、日本語、Français
- **安全机制** — Session HMAC 签名、登录/注册验证码、bcrypt 密码加密
- **单二进制部署** — 模板、静态资源、多语言词典全部通过 `go:embed` 嵌入

## 技术栈

- Go 1.26、[Gin](https://github.com/gin-gonic/gin)
- MySQL + [GORM](https://gorm.io)
- 服务端渲染 HTML 模板 + 原生 JS/CSS（无前端构建步骤）

## 快速开始

### 1. 环境要求

- Go 1.26+
- MySQL 8.x（需预先创建空库）：

```sql
CREATE DATABASE doc_share DEFAULT CHARSET utf8mb4;
```

### 2. 配置

所有配置在 `config.yaml`，均可通过环境变量覆盖。占位符支持 `${VAR}` 和 `${VAR:-默认值}` 语法。

| 环境变量 | 必填 | 说明 |
|---|---|---|
| `DOC_SHARE_SESSION_SECRET` | 是 | Session 与分享凭证的 HMAC 签名密钥（≥32 位随机字符串），未设置时服务拒绝启动 |
| `DOC_SHARE_DB_DSN` | 否 | MySQL DSN（默认 `root:root@tcp(127.0.0.1:3306)/doc_share?charset=utf8mb4&parseTime=True&loc=Local`） |
| `DOC_SHARE_PORT` | 否 | 监听地址（默认 `:8080`） |
| `DOC_SHARE_ADMIN_USER` | 否 | 首次启动自动创建的管理员用户名（默认 `admin`） |
| `DOC_SHARE_ADMIN_PASSWORD` | 否 | 管理员初始密码（默认 `admin123`） |
| `DOC_SHARE_UPLOAD_DIR` | 否 | 图片上传目录（默认 `./uploads`） |
| `DOC_SHARE_UPLOAD_MAX_MB` | 否 | 单张图片大小上限 MB（默认 `10`） |
| `DOC_SHARE_DEV` | 否 | `true` = 模板/静态资源直读磁盘，前端热载 |
| `DOC_SHARE_DB_LOG` | 否 | `true` = 开启 GORM SQL 日志 |

> 仅限本地调试：设置 `DOC_SHARE_ALLOW_INSECURE_DEFAULTS=true` 可临时放行空的 session secret。

### 3. 启动

```bash
export DOC_SHARE_SESSION_SECRET="change-me-to-a-random-string-32chars"
go run .
# 或编译后运行
go build -o doc-share .
./doc-share -config config.yaml
```

访问 `http://localhost:8080`，使用默认管理员账号登录（请立即修改密码）。

## 目录结构

```
main.go                 入口，嵌入静态资源（go:embed）
config.yaml             配置文件，支持环境变量占位符
internal/
  config/               YAML 加载 + ${VAR} 展开
  database/             GORM/MySQL 初始化与自动迁移
  handler/              HTTP 处理器（后台、开放 API、分享、认证、上传）
  middleware/           认证 / API Key / 多语言中间件
  model/                GORM 数据模型
  router/               路由注册
  session/              HMAC Session 签名
  i18n/                 多语言词典加载
  convert/              PDF / doc / docx → Markdown 转换器
  util/                 工具函数
web/
  templates/            HTML 模板
  static/               CSS / JS
  locales/              en-US, zh-CN, zh-TW, ja-JP, fr-FR
```

## 开发模式

设置 `DOC_SHARE_DEV=true` 后，模板与静态资源直读 `web/` 目录——修改前端只需刷新浏览器，无需重新编译或重启。生产环境务必保持 `false`（使用嵌入资源）。

## API 一览

| 模块 | 端点 |
|---|---|
| 认证 | `GET/POST /login`、`GET/POST /register`、`GET /captcha`、`GET /logout` |
| 后台页面 | `/admin`、`/admin/docs`、`/admin/projects`、`/admin/categories`、`/admin/apikeys`、`/admin/users` |
| 文档管理 | `POST/PUT/DELETE /api/docs`、`POST /api/docs/:id/share` |
| 分享访问 | `GET/POST /s/:token` |
| 开放 API（API Key） | `/open/docs`、`/open/projects`、`/open/categories`（完整增删改查） |
| 上传 / 转换 | `POST /api/upload`、`POST /api/convert` |

## 许可证

见 [LICENSE](LICENSE)。
