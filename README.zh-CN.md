# <img src="web/static/logo-mark.svg" height="36" alt="DocShare logo"/> DocShare

中文说明 | [English](README.md)

基于 Go + Gin 的轻量级自托管文档分享平台。用 Markdown 编写文档，按项目/分类组织，通过带令牌的链接对外分享；支持密码保护、申请查看、协作评论与修订，以及开放 API / MCP 接入。

## 功能一览

### 文档编写与管理

- **Markdown 编辑器** — 工具栏（加粗/列表/代码/链接/图片/表格等）、实时预览、XSS 净化渲染
- **图片上传** — 粘贴或拖拽上传；存储可选本地磁盘（`/uploads`）或 RustFS（S3 兼容）
- **文档导入** — `PDF` / `doc` / `docx` 纯内存转为 Markdown，不落盘临时文件
- **列表与筛选** — 按标题搜索，按项目、分类、分享状态、归属（我的 / 项目共享）过滤；分页浏览
- **预览** — 管理端以读者视图预览，不计浏览数
- **编辑占用提示** — 心跳检测他人正在编辑同一文档

### 分享与访问

- **分享链接** — `/s/:token` 公开访问，可随时关闭撤销
- **访问密码** — 可选密码；支持生成 / 重置；复制时附带标题、链接与密码
- **有效期** — 永久 / 1 天 / 7 天 / 30 天
- **申请查看** — 有密码时，访客（登录或游客）可填写称呼申请访问；属主在分享设置或仪表盘审批；通过后免密阅读（站点级开关，管理员配置）
- **登录可编辑** — 分享可允许已登录用户在阅读页直接改文档
- **浏览量** — 阅读页累计访问次数

### 协作

- **评论** — 分享页支持登录用户与游客评论（限流）；文档属主/管理员可删除
- **修订历史** — 分享编辑覆盖前自动快照；编辑器内查看并回滚
- **申请通过 = 已解锁** — 与输对密码同等，可阅读、评论；若开启「可编辑」亦可保存

### 项目与分类

- **项目** — 个人项目 CRUD；文档归入项目；按名称/拥有者筛选（管理员）
- **项目成员** — 邀请用户，角色 **查看** / **编辑**；成员可见项目内文档；可主动退出；仅属主管理成员
- **分类** — 个人分类 CRUD、排序；删除后文档变为未分类

### 账户与权限

- **注册 / 登录** — 验证码；注册方式由管理员配置：用户名 / 邮箱（SMTP 验证码）/ 手机号（格式校验）
- **个人资料** — 昵称、邮箱、手机、头像；修改自己的密码
- **角色** — `admin`（全站管理）/ `viewer`（管理自己的文档与空间）；可启用/停用用户
- **用户管理**（管理员）— 创建、编辑、删除、重置密码、改角色与状态

### 仪表盘与后台

- **仪表盘** — 文档数、分享数、用户数（管理员）、关联项目数；最近更新文档
- **待审申请** — 未处理的查看申请统计与弹窗列表，可直接通过/拒绝
- **系统设置**（管理员）— 站点名称、Logo、对外域名；开放「申请查看」；注册方式；SMTP（可测发信）；文件存储本地 / RustFS（可测连通）
- **多语言** — 简体中文、繁體中文、English、日本語、Français

### 开放能力

- **API 密钥** — 个人创建密钥；启用/停用、重置 Secret（仅显示一次）、删除
- **开放 API** — `/openapi/v1`，HMAC（AppKey + 时间戳 + nonce + 签名）认证，文档/项目/分类完整 CRUD；权限等同密钥属主
- **站内 API 文档** — `/admin/apidoc` 说明签名算法与接口
- **MCP 服务** — 零依赖 Node stdio 服务，供 CodeBuddy / Claude / Cursor 等通过工具管理文档

## 技术栈

- Go 1.26、[Gin](https://github.com/gin-gonic/gin)
- MySQL + [GORM](https://gorm.io)
- 服务端渲染 HTML 模板 + 原生 JS/CSS（无前端构建步骤）
- 模板、静态资源、多语言词典通过 `go:embed` 嵌入，单二进制部署
- Session HMAC 签名、bcrypt 密码、分享凭证签名

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

站点名称、Logo、注册方式、SMTP、RustFS 等可在登录后于 **系统设置**（`/admin/settings`）调整，无需改配置文件。

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
  storage/              本地 / RustFS 存储
  i18n/                 多语言词典加载
  convert/              PDF / doc / docx → Markdown 转换器
  util/                 工具函数
web/
  templates/            HTML 模板
  static/               CSS / JS
  locales/              en-US, zh-CN, zh-TW, ja-JP, fr-FR
mcp/
  mcp-server.js         零依赖 MCP 服务器（Node，stdio），包装开放 API 供 AI 客户端调用
```

## 开发模式

设置 `DOC_SHARE_DEV=true` 后，模板与静态资源直读 `web/` 目录——修改前端只需刷新浏览器，无需重新编译或重启。生产环境务必保持 `false`（使用嵌入资源）。

## 主要页面与接口

| 模块 | 路径 |
|---|---|
| 落地页 | `GET /` |
| 认证 | `GET/POST /login`、`GET/POST /register`、`POST /register/email-code`、`GET /captcha`、`GET/POST /logout` |
| 后台页面 | `/admin`、`/admin/docs`、`/admin/projects`、`/admin/categories`、`/admin/apikeys`、`/admin/apidoc`、`/admin/users`、`/admin/settings` |
| 文档 API | `POST/PUT/DELETE /admin/api/docs`、分享 `POST/DELETE /admin/api/docs/:id/share` |
| 访问申请 | `POST /s/:token/access-request`；列表/审批 `/admin/api/access-requests`、`/admin/api/docs/:id/access-requests` |
| 分享访问 | `GET/POST /s/:token`；评论 `GET/POST /s/:token/comments`；编辑保存 `PUT /s/:token/content` |
| 开放 API | `/openapi/v1/docs`、`/projects`、`/categories`（完整增删改查，HMAC） |
| 上传 / 转换 | `POST /admin/api/upload`、`POST /admin/api/convert` |

更完整的签名说明与错误码见站内 **API 文档**（`/admin/apidoc`）。

## MCP 服务（AI 客户端接入）

`mcp/mcp-server.js` 是一个零依赖的 MCP 服务器（Node >= 18，stdio 传输），把开放 API 包装成 MCP 工具，供 CodeBuddy / Claude Desktop / Cursor 等 AI 客户端直接管理文档、项目和分类。

### 1. 准备密钥

在 DocShare 后台 `/admin/apikeys` 创建 API 密钥，得到 `AppKey`（`ak_` 开头）和 `Secret`（`sk_` 开头，仅创建时显示一次）。密钥属主即文档所有者，权限与后台一致。

### 2. 客户端配置

在 MCP 客户端的 `mcpServers` 配置中加入：

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

也可以不设环境变量，改为在 `mcp/` 目录创建 `mcp.config.json`（参考 `mcp/mcp.config.example.json`，已被 `.gitignore` 忽略）：

```json
{
  "baseUrl": "http://127.0.0.1:9000",
  "appKey": "ak_xxx",
  "secret": "sk_xxx"
}
```

环境变量优先于配置文件；`DOC_SHARE_BASE_URL` 默认 `http://127.0.0.1:9000`。

### 3. 可用工具

| 工具 | 说明 |
|---|---|
| `docshare_list_docs` | 文档分页列表（不含正文），可按 `project_id` / `category_id` 过滤 |
| `docshare_get_doc` | 读取文档完整内容（含 Markdown 正文） |
| `docshare_create_doc` / `docshare_update_doc` / `docshare_delete_doc` | 文档增删改（更新时 `content` 整体覆盖，`title` 留空表示不修改） |
| `docshare_list_projects` / `docshare_create_project` / `docshare_update_project` / `docshare_delete_project` | 项目管理（删除后其下文档回到未分组） |
| `docshare_list_categories` / `docshare_create_category` / `docshare_update_category` / `docshare_delete_category` | 分类管理（删除后其下文档变为未分类） |

## 许可证

见 [LICENSE](LICENSE)。
