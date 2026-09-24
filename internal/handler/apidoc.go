package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// apiParam 接口参数说明（In: path / query / body）
type apiParam struct {
	Name    string
	In      string
	DescKey string
}

// apiEndpoint 单个接口：方法徽章、路径、说明（i18n key）、参数表、请求/响应示例
type apiEndpoint struct {
	Method      string
	Class       string // 方法小写，用于 CSS 配色
	Path        string
	DescKey     string // 卡片说明（较详细）
	LabelKey    string // 侧边目录短标签（业务化命名，如「新增分类」）
	Anchor      string // 页内锚点 id，供侧边目录跳转/滚动高亮
	Params      []apiParam
	ReqExample  string // 请求示例（格式化 JSON）
	RespExample string // 响应示例（格式化 JSON）
}

// apiGroup 按资源分组的接口列表
type apiGroup struct {
	GroupKey string
	Anchor   string // 分组标题锚点
	Items    []apiEndpoint
}

// apiErrorRow 错误码表行：HTTP 状态码 + error 原文
type apiErrorRow struct {
	Code int
	Msg  string
}

// apiErrorGroup 错误码分组（通用 / 业务）
type apiErrorGroup struct {
	Key  string
	Rows []apiErrorRow
}

const p = "/openapi/v1"

// openAPIGroups 开放平台接口清单，与 router.go 的 /openapi/v1 路由一一对应。
// 新增接口时同步维护此表即可，模板自动渲染。
func openAPIGroups() []apiGroup {
	return []apiGroup{
		{GroupKey: "apidoc.gCats", Items: []apiEndpoint{
			{Method: "GET", Path: p + "/categories", DescKey: "ep.catList", LabelKey: "ep.tCatList",
				RespExample: `{
  "data": [
    {
      "id": 1,
      "name": "产品文档",
      "sort": 0,
      "owner_id": 2,
      "doc_count": 5
    }
  ]
}`},
			{Method: "POST", Path: p + "/categories", DescKey: "ep.catCreate", LabelKey: "ep.tCatCreate",
				ReqExample: `{
  "name": "产品文档",
  "sort": 0
}`,
				RespExample: `{
  "data": {
    "id": 1,
    "name": "产品文档",
    "sort": 0,
    "owner_id": 2,
    "doc_count": 0
  }
}`},
			{Method: "PUT", Path: p + "/categories/:id", DescKey: "ep.catUpdate", LabelKey: "ep.tCatUpdate",
				ReqExample: `{
  "name": "产品文档（改名）",
  "sort": 10
}`,
				RespExample: `{
  "data": {
    "id": 1,
    "name": "产品文档（改名）",
    "sort": 10,
    "owner_id": 2,
    "doc_count": 5
  }
}`},
			{Method: "DELETE", Path: p + "/categories/:id", DescKey: "ep.catDelete", LabelKey: "ep.tCatDelete",
				RespExample: `{
  "ok": true
}`},
		}},
		{GroupKey: "apidoc.gProjects", Items: []apiEndpoint{
			{Method: "GET", Path: p + "/projects", DescKey: "ep.projList", LabelKey: "ep.tProjList",
				RespExample: `{
  "data": [
    {
      "id": 3,
      "name": "2026 规划",
      "description": "年度规划文档",
      "owner_id": 2,
      "doc_count": 8,
      "created_at": "2026-01-05T10:00:00+08:00",
      "updated_at": "2026-09-01T14:30:00+08:00"
    }
  ]
}`},
			{Method: "POST", Path: p + "/projects", DescKey: "ep.projCreate", LabelKey: "ep.tProjCreate",
				ReqExample: `{
  "name": "2026 规划",
  "description": "年度规划文档"
}`,
				RespExample: `{
  "data": {
    "id": 3,
    "name": "2026 规划",
    "description": "年度规划文档",
    "owner_id": 2,
    "doc_count": 0,
    "created_at": "2026-09-24T09:00:00+08:00",
    "updated_at": "2026-09-24T09:00:00+08:00"
  }
}`},
			{Method: "PUT", Path: p + "/projects/:id", DescKey: "ep.projUpdate", LabelKey: "ep.tProjUpdate",
				ReqExample: `{
  "name": "2026 规划（修订）",
  "description": "年度规划与里程碑"
}`,
				RespExample: `{
  "data": {
    "id": 3,
    "name": "2026 规划（修订）",
    "description": "年度规划与里程碑",
    "owner_id": 2,
    "doc_count": 8,
    "updated_at": "2026-09-24T10:00:00+08:00"
  }
}`},
			{Method: "DELETE", Path: p + "/projects/:id", DescKey: "ep.projDelete", LabelKey: "ep.tProjDelete",
				RespExample: `{
  "ok": true
}`},
		}},
		{GroupKey: "apidoc.gDocs", Items: []apiEndpoint{
			{Method: "GET", Path: p + "/docs", DescKey: "ep.docList", LabelKey: "ep.tDocList",
				RespExample: `{
  "data": [
    {
      "id": 12,
      "title": "接入指南",
      "slug": "aB3xY9kQ",
      "owner_id": 2,
      "project_id": 3,
      "category_id": 1,
      "is_shared": false,
      "view_count": 16,
      "created_at": "2026-08-12T09:20:00+08:00",
      "updated_at": "2026-09-20T18:02:00+08:00"
    }
  ],
  "total": 42,
  "page": 1,
  "size": 20
}`},
			{Method: "GET", Path: p + "/docs/:id", DescKey: "ep.docGet", LabelKey: "ep.tDocGet",
				RespExample: `{
  "data": {
    "id": 12,
    "title": "接入指南",
    "slug": "aB3xY9kQ",
    "content": "# 接入指南\n\n正文 Markdown…",
    "owner_id": 2,
    "project_id": 3,
    "category_id": 1,
    "is_shared": false,
    "view_count": 16,
    "created_at": "2026-08-12T09:20:00+08:00",
    "updated_at": "2026-09-20T18:02:00+08:00"
  }
}`},
			{Method: "POST", Path: p + "/docs", DescKey: "ep.docCreate", LabelKey: "ep.tDocCreate",
				ReqExample: `{
  "title": "接入指南",
  "content": "# 接入指南\n\n正文 Markdown…",
  "project_id": 3,
  "category_id": 1
}`,
				RespExample: `{
  "data": {
    "id": 12,
    "title": "接入指南",
    "slug": "aB3xY9kQ",
    "content": "# 接入指南\n\n正文 Markdown…",
    "owner_id": 2,
    "project_id": 3,
    "category_id": 1,
    "is_shared": false,
    "view_count": 0
  }
}`},
			{Method: "PUT", Path: p + "/docs/:id", DescKey: "ep.docUpdate", LabelKey: "ep.tDocUpdate",
				ReqExample: `{
  "title": "接入指南（修订）",
  "content": "# 接入指南 v2\n\n更新后的正文…",
  "project_id": 0,
  "category_id": 1
}`,
				RespExample: `{
  "data": {
    "id": 12,
    "title": "接入指南（修订）",
    "slug": "aB3xY9kQ",
    "content": "# 接入指南 v2\n\n更新后的正文…",
    "owner_id": 2,
    "project_id": 0,
    "category_id": 1,
    "is_shared": false,
    "view_count": 16
  }
}`},
			{Method: "DELETE", Path: p + "/docs/:id", DescKey: "ep.docDelete", LabelKey: "ep.tDocDelete",
				RespExample: `{
  "ok": true
}`},
		}},
	}
}

// openAPIErrors 错误码清单：error 信息为 handler 返回的中文原文。
// 通用 = openapi 签名中间件（openapi.go）；业务 = 各 CRUD handler（category/project/doc.go）。
func openAPIErrors() []apiErrorGroup {
	return []apiErrorGroup{
		{Key: "apidoc.errCommon", Rows: []apiErrorRow{
			{401, "缺少签名头：X-App-Key / X-Timestamp / X-Nonce / X-Signature"},
			{401, "时间戳无效或与服务器偏差超过 5 分钟"},
			{401, "nonce 长度需在 8~64 之间"},
			{401, "AppKey 不存在或已禁用"},
			{400, "读取请求体失败或超过 2MB"},
			{401, "签名校验失败"},
			{401, "nonce 已使用，疑似重放请求"},
			{401, "密钥属主不存在或已禁用"},
		}},
		{Key: "apidoc.errBiz", Rows: []apiErrorRow{
			{400, "参数错误"},
			{400, "标题不能为空"},
			{400, "项目或分类非法"},
			{400, "项目非法或无权归属到该项目"},
			{400, "分类非法或无权归属到该分类"},
			{400, "创建失败，你的分类名已存在"},
			{400, "更新失败，你的分类名已存在"},
			{400, "分类 id 非法 / 项目 id 非法"},
			{403, "无权操作他人的分类 / 项目 / 文档"},
			{404, "分类不存在 / 项目不存在 / 文档不存在"},
			{500, "创建失败 / 更新失败 / 删除失败"},
		}},
	}
}

// buildAPIGroups 为接口与分组生成页内锚点
func buildAPIGroups() []apiGroup {
	groups := openAPIGroups()
	for gi := range groups {
		g := &groups[gi]
		g.Anchor = "epg-" + strings.ToLower(strings.TrimPrefix(g.GroupKey, "apidoc.g"))
		for ii := range g.Items {
			it := &g.Items[ii]
			suffix := strings.TrimPrefix(it.Path, p) // 如 /categories/:id
			anchor := strings.ToLower(strings.ReplaceAll(it.Method+suffix, "/", "-"))
			anchor = strings.ReplaceAll(anchor, ":", "")
			it.Anchor = anchor
			it.Class = strings.ToLower(it.Method)
		}
	}
	return groups
}

// APIDocPage 开放平台 API 对接文档页
func (a *App) APIDocPage(c *gin.Context) {
	a.render(c, "apidoc.html", gin.H{
		"title":     "API 文档",
		"groups":    buildAPIGroups(),
		"errGroups": openAPIErrors(),
		"BaseURL":   a.siteBaseURL(c), // 系统设置的系统域名，留空取当前访问域名
	})
}
