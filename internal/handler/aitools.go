package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"doc-share/internal/llm"
	"doc-share/internal/model"
	"doc-share/internal/search"
	"doc-share/internal/util"
	"doc-share/internal/webfetch"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// AI 工具层：把站内数据接口注册为 function calling 工具。
// 可见范围与文档列表页一致：自己的文档 ∪ 所参与项目内的文档（管理员全部）。
// 工具结果不落库，仅作为当轮上下文注入。

const (
	aiToolMaxResult    = 8000      // 单次工具结果字符上限（防止撑爆上下文）
	aiPrereadMaxChars  = 40000     // 系统预读单篇上限（拼齐全量，避免 Flash 因分页停等）
	aiToolMaxDocs      = 10        // search_documents 返回条数上限
	aiMaxContentBytes  = 200 << 10 // AI 写入文档正文的大小上限（200KB）
	aiMaxCreatesPerReq = 3         // 单次对话内 create_document 次数上限
	aiMaxCreatesPerDay = 50        // 单用户单日 AI 创建文档数上限
)

// aiApprovalTools 执行前必须经用户在会话中确认的敏感工具
var aiApprovalTools = map[string]bool{
	"delete_document":       true,
	"restore_document":      true,
	"restore_revision":      true,
	"delete_skill":          true,
	"delete_project":        true,
	"delete_category":       true,
	"remove_project_member": true,
	"delete_template":       true,
	"delete_comment":        true,
	"purge_document":        true,
	"review_access_request": true,
}

// aiNeedsApproval 判断一次工具调用是否需要用户确认：
// 删除/恢复/回滚一律需要；set_share 仅在"开启公开分享"时需要（关闭/改密码不弹窗）。
func aiNeedsApproval(tc llm.ToolCallMsg) bool {
	if aiApprovalTools[tc.Function.Name] {
		return true
	}
	if tc.Function.Name == "set_share" || tc.Function.Name == "set_project_share" {
		var args struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		return args.Enabled
	}
	if tc.Function.Name == "add_project_member" || tc.Function.Name == "update_project_member" {
		var args struct {
			Role string `json:"role"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		return strings.TrimSpace(args.Role) == model.MemberRoleEdit
	}
	return false
}

// chatTools 返回当前用户可用的工具定义、执行器，以及仅服务端预读用的 injectRead。
// 工具结果中一律不出现真实 URL，链接用占位符 {{doc:<id>}} / {{share:<id>}} 表示，
// 由服务端在流式输出时替换为真实链接（防止模型编造/改写 URL，见 chat.go 的 newLinkResolver）。
// injectRead 不暴露给模型；exec 若收到未注册名（含旧内部名 __read_document_full）一律未知工具。
func (a *App) chatTools(user *model.User) (
	defs []llm.Tool,
	exec func(name, argsJSON string) (string, error),
	injectRead func(id int64, maxChars int, query string) (string, error),
) {
	// 用户可见的项目 ID 只查一次，scope 闭包复用
	var memberPids []uint
	if !user.IsAdmin() {
		a.DB.Model(&model.ProjectMember{}).Where("user_id = ?", user.ID).Pluck("project_id", &memberPids)
	}
	scope := func() *gorm.DB {
		tx := a.DB.Model(&model.Document{})
		if !user.IsAdmin() {
			if len(memberPids) == 0 {
				tx = tx.Where("owner_id = ?", user.ID)
			} else {
				tx = tx.Where("owner_id = ? OR project_id IN ?", user.ID, memberPids)
			}
		}
		return tx
	}
	// 项目可见范围同样复用
	projectsScope := func() *gorm.DB {
		tx := a.DB.Model(&model.Project{})
		if !user.IsAdmin() {
			if len(memberPids) == 0 {
				tx = tx.Where("owner_id = ?", user.ID)
			} else {
				tx = tx.Where("owner_id = ? OR id IN ?", user.ID, memberPids)
			}
		}
		return tx
	}
	// 是否有该文档的编辑权限（属主 / 管理员 / 项目 edit 角色）
	canEditDoc := func(doc *model.Document) bool {
		return user.IsAdmin() || doc.OwnerID == user.ID || a.projectRole(doc, user.ID) == model.MemberRoleEdit
	}

	defs = []llm.Tool{
		{Type: "function", Function: llm.ToolFunc{
			Name:        "search_documents",
			Description: "按关键词搜索当前用户可见的平台文档，返回标题、路径等元数据。需要了解平台有哪些文档时先调用本工具。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "关键词，匹配标题、路径或正文"},
					"limit": map[string]any{"type": "integer", "description": "返回条数上限，默认 5，最大 10"},
				},
				"required": []string{"query"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "read_document",
			Description: "按文档 ID 读取当前用户可见文档的 Markdown 正文。长文档用 offset/length 分页读完，返回头部标明全文总字符数与下一页 offset。必须以返回的 content_version 作为后续 update_document 的 base_version。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "integer", "description": "search_documents 返回的文档 ID"},
					"offset": map[string]any{"type": "integer", "description": "起始字符偏移（按字符计，默认 0）"},
					"length": map[string]any{"type": "integer", "description": "本次读取字符数（默认/上限 8000）"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_projects",
			Description: "列出当前用户可见的项目（自己的，以及作为成员参与的）。创建、修改、删除只能作用于自己的项目。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "create_project",
			Description: "创建项目，归属当前用户。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string", "description": "项目名称（必填，≤100 字）"},
					"description": map[string]any{"type": "string", "description": "描述（可选，≤500 字）"},
				},
				"required": []string{"name"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "update_project",
			Description: "修改自己的项目名称和描述（管理员可改全部）。未传 description 则清空描述。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "integer", "description": "项目 ID"},
					"name":        map[string]any{"type": "string", "description": "项目名称"},
					"description": map[string]any{"type": "string", "description": "描述（可选）"},
				},
				"required": []string{"id", "name"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "delete_project",
			Description: "删除自己的项目（管理员可删全部）。其下文档回到未分组，不删除文档。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "项目 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_categories",
			Description: "列出当前用户自己的分类（与 MCP/OpenAPI 一致，不含他人分类），含排序值。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "create_category",
			Description: "创建分类，名称在当前用户的分类中必须唯一。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "分类名称（必填，≤50 字）"},
					"sort": map[string]any{"type": "integer", "description": "排序值，越小越靠前（可选，默认 0）"},
				},
				"required": []string{"name"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "update_category",
			Description: "修改自己的分类名称或排序（管理员可改全部）。未传 sort 则保持原排序。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":   map[string]any{"type": "integer", "description": "分类 ID"},
					"name": map[string]any{"type": "string", "description": "分类名称"},
					"sort": map[string]any{"type": "integer", "description": "排序值（可选，不传则不改）"},
				},
				"required": []string{"id", "name"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "delete_category",
			Description: "删除自己的分类（管理员可删全部）。其下文档变为未分类，不删除文档。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "分类 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "read_webpage",
			Description: "读取一个公网 http(s) 页面的可见正文（去掉脚本和样式）。拒绝内网、本机和保留地址。长页面用 offset/length 分页。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url":    map[string]any{"type": "string", "description": "以 http:// 或 https:// 开头的公网地址"},
					"offset": map[string]any{"type": "integer", "description": "起始字符偏移（按字符计，默认 0）"},
					"length": map[string]any{"type": "integer", "description": "本次读取字符数（默认/上限 8000）"},
				},
				"required": []string{"url"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_documents",
			Description: "列出当前用户可见的文档（可按项目或分类过滤），返回元数据列表。不确定关键词时用它浏览全部文档。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":  map[string]any{"type": "integer", "description": "只列该项目下的文档（可选）"},
					"category_id": map[string]any{"type": "integer", "description": "只列该分类下的文档（可选）"},
					"limit":       map[string]any{"type": "integer", "description": "返回条数上限，默认 20，最大 50"},
				},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "get_share_link",
			Description: "获取文档的站内访问链接与分享链接（若已开启分享），包含分享是否可编辑/是否有密码/过期时间。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "文档 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "set_share",
			Description: "开启或关闭文档的公开分享链接（仅文档属主可用）。开启后返回 /s/<token> 分享链接；未传 can_edit/password/remove_password 时保留原有设置。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":              map[string]any{"type": "integer", "description": "文档 ID"},
					"enabled":         map[string]any{"type": "boolean", "description": "true 开启分享，false 关闭"},
					"can_edit":        map[string]any{"type": "boolean", "description": "登录用户通过分享链接是否可编辑（可选，不传保留原设置）"},
					"password":        map[string]any{"type": "string", "description": "访问密码（可选，明文传入后服务端 bcrypt 存储）"},
					"remove_password": map[string]any{"type": "boolean", "description": "true 表示清除已设密码（可选）"},
					"expire_days":     map[string]any{"type": "integer", "description": "有效期天数（可选；不传保持原值，0=永不过期）"},
				},
				"required": []string{"id", "enabled"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "revisions_history",
			Description: "列出文档的历史修订版本（ID/标签/说明/编辑者/时间），用于了解版本脉络或寻找要恢复的版本。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "文档 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "restore_revision",
			Description: "把文档正文恢复为指定历史修订的内容（会覆盖当前正文，当前内容会先存为新修订；需要用户确认后执行）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "integer", "description": "文档 ID"},
					"revision_id": map[string]any{"type": "integer", "description": "revisions_history 返回的修订 ID"},
				},
				"required": []string{"id", "revision_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "delete_document",
			Description: "把文档移入回收站（软删除，可在回收站恢复；需要用户确认后执行）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "文档 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_trash",
			Description: "列出回收站里的文档（自己删除的；管理员可看全部）。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "restore_document",
			Description: "从回收站恢复文档（需要用户确认后执行）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "回收站里的文档 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_templates",
			Description: "列出当前用户可用的文档模板（自己的或所在项目共用的）。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "get_template",
			Description: "读取可用模板的完整内容（markdown / 画布 JSON / drawio XML），可作为创建文档的范例。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "模板 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "create_template",
			Description: "创建文档模板。type 支持 markdown / mindmap / board / drawio。" + aiCanvasDocHint,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string", "description": "模板名称（≤100 字）"},
					"type":        map[string]any{"type": "string", "description": "markdown（默认）/ mindmap / board / drawio"},
					"content":     map[string]any{"type": "string", "description": "模板内容"},
					"project_id":  map[string]any{"type": "integer", "description": "绑定项目后成员可共用（可选，须自己拥有或参与）"},
					"category_id": map[string]any{"type": "integer", "description": "绑定分类（可选，须自己的）"},
				},
				"required": []string{"name"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "update_template",
			Description: "更新自己的模板（管理员可改全部）。未传字段不修改；project_id / category_id 传 0 解除绑定。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "integer", "description": "模板 ID"},
					"name":        map[string]any{"type": "string", "description": "模板名称"},
					"type":        map[string]any{"type": "string", "description": "markdown / mindmap / board / drawio"},
					"content":     map[string]any{"type": "string", "description": "模板内容"},
					"project_id":  map[string]any{"type": "integer", "description": "绑定项目（0=个人私有）"},
					"category_id": map[string]any{"type": "integer", "description": "绑定分类（0=不绑定）"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "delete_template",
			Description: "删除自己的模板（不影响已用模板创建的文档）。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "模板 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "apply_template",
			Description: "按模板创建一篇新文档（支持 markdown / mindmap / board / drawio；非覆盖，直接执行）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"template_id": map[string]any{"type": "integer", "description": "list_templates 返回的模板 ID"},
					"title":       map[string]any{"type": "string", "description": "新文档标题（可选，默认用模板名）"},
					"project_id":  map[string]any{"type": "integer", "description": "归属项目 ID（可选，默认归属模板原项目或个人）"},
					"category_id": map[string]any{"type": "integer", "description": "归属分类 ID（可选）"},
				},
				"required": []string{"template_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "update_document",
			Description: "整体覆盖当前用户有编辑权限的文档的标题和/或正文（Markdown）；文档属主还可改 project_id / category_id（同事务写入）。修改正文必须传 base_version（取自 read_document 返回），版本不匹配会被拒绝；只改标题或归属时无需 base_version。project_id / category_id 传 0 表示清空，且只能挂到自己的项目或分类。局部修改请改用 replace_section。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":           map[string]any{"type": "integer", "description": "文档 ID"},
					"title":        map[string]any{"type": "string", "description": "新标题（可选）"},
					"content":      map[string]any{"type": "string", "description": "新的完整 Markdown 正文（可选；必须给出完整内容，不是增量）"},
					"base_version": map[string]any{"type": "integer", "description": "修改正文时必填：read_document 返回的 content_version，防止凭记忆覆盖丢失内容"},
					"project_id":   map[string]any{"type": "integer", "description": "归属项目 ID（可选，0=未分组，必须是自己的项目）"},
					"category_id":  map[string]any{"type": "integer", "description": "归属分类 ID（可选，0=未分类，必须是自己的分类）"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "replace_section",
			Description: "在文档正文中做局部替换：把唯一匹配的 old_text 替换为 new_text（服务端在最新正文上执行，不会影响其他内容）。修改文档正文的首选方式，无需提供全文。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":         map[string]any{"type": "integer", "description": "文档 ID"},
					"old_text":   map[string]any{"type": "string", "description": "要被替换的原文片段，必须与正文逐字一致且唯一；命中多处时请扩写上下文或用 occurrence 指定第几处"},
					"new_text":   map[string]any{"type": "string", "description": "替换后的新内容；删除内容时传空字符串"},
					"occurrence": map[string]any{"type": "integer", "description": "替换第几处匹配（1 起，可选；不传时要求 old_text 唯一匹配）"},
				},
				"required": []string{"id", "old_text", "new_text"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "create_document",
			Description: "创建文档。type 默认 markdown，也可 mindmap / board / drawio。project_id / category_id 可选且必须是自己的。" + aiCanvasDocHint,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":       map[string]any{"type": "string", "description": "文档标题"},
					"type":        map[string]any{"type": "string", "description": "markdown（默认）/ mindmap / board / drawio；html 整站不支持"},
					"content":     map[string]any{"type": "string", "description": "markdown 正文 / 画布 JSON / drawio XML"},
					"project_id":  map[string]any{"type": "integer", "description": "归属项目 ID（可选，必须是自己的项目）"},
					"category_id": map[string]any{"type": "integer", "description": "归属分类 ID（可选，必须是自己的分类）"},
				},
				"required": []string{"title"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "duplicate_document",
			Description: "复制一篇可见且有编辑权限的文档（含画布）。HTML 整站请在页面上复制。不复制分享。计入与 create_document 相同的创建配额。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "源文档 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "purge_document",
			Description: "从回收站彻底删除文档（不可恢复）。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "回收站中的文档 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "label_revision",
			Description: "给文档历史修订打版本标签与说明（如 v1.0.1）；空 label 表示取消标签。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "integer", "description": "文档 ID"},
					"revision_id": map[string]any{"type": "integer", "description": "revisions_history 返回的修订 ID"},
					"label":       map[string]any{"type": "string", "description": "版本标签（≤32 字，空=取消）"},
					"note":        map[string]any{"type": "string", "description": "说明（≤500 字，可选）"},
				},
				"required": []string{"id", "revision_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_comments",
			Description: "列出文档评论（含回复关系）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "文档 ID"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "add_comment",
			Description: "在文档下发表评论（需编辑权限）；可回复顶层评论。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":        map[string]any{"type": "integer", "description": "文档 ID"},
					"content":   map[string]any{"type": "string", "description": "评论正文（≤1000 字）"},
					"parent_id": map[string]any{"type": "integer", "description": "回复的顶层评论 ID（可选）"},
				},
				"required": []string{"id", "content"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "delete_comment",
			Description: "删除文档评论及其回复（仅文档属主/管理员）。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":         map[string]any{"type": "integer", "description": "文档 ID"},
					"comment_id": map[string]any{"type": "integer", "description": "评论 ID"},
				},
				"required": []string{"id", "comment_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_project_members",
			Description: "列出项目成员（含属主）。属主/管理员/成员可读。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "integer", "description": "项目 ID"},
				},
				"required": []string{"project_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "add_project_member",
			Description: "把用户加入自己的项目（管理员可操作全部项目）。role=view 或 edit；设为 edit 需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "integer", "description": "项目 ID"},
					"username":   map[string]any{"type": "string", "description": "用户名（与 user_id 二选一）"},
					"user_id":    map[string]any{"type": "integer", "description": "用户 ID（与 username 二选一）"},
					"role":       map[string]any{"type": "string", "description": "view（默认）或 edit"},
				},
				"required": []string{"project_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "update_project_member",
			Description: "修改项目成员角色（仅项目属主/管理员）。改为 edit 需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "integer", "description": "项目 ID"},
					"user_id":    map[string]any{"type": "integer", "description": "成员用户 ID"},
					"role":       map[string]any{"type": "string", "description": "view 或 edit"},
				},
				"required": []string{"project_id", "user_id", "role"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "remove_project_member",
			Description: "移除项目成员（仅项目属主/管理员）。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "integer", "description": "项目 ID"},
					"user_id":    map[string]any{"type": "integer", "description": "成员用户 ID"},
				},
				"required": []string{"project_id", "user_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "get_project_share",
			Description: "查看项目分享是否开启及权限设置（不含原始 token；链接用占位符）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id": map[string]any{"type": "integer", "description": "项目 ID"},
				},
				"required": []string{"project_id"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "set_project_share",
			Description: "开启或关闭项目公开分享（仅项目属主/管理员）。开启时需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project_id":      map[string]any{"type": "integer", "description": "项目 ID"},
					"enabled":         map[string]any{"type": "boolean", "description": "true 开启，false 关闭"},
					"can_edit":        map[string]any{"type": "boolean", "description": "登录用户是否可编辑（可选）"},
					"password":        map[string]any{"type": "string", "description": "访问密码（可选）"},
					"remove_password": map[string]any{"type": "boolean", "description": "true 清除已设密码"},
					"expire_days":     map[string]any{"type": "integer", "description": "有效期天数（不传保持原值，0=永久）"},
				},
				"required": []string{"project_id", "enabled"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_access_requests",
			Description: "列出文档或项目的分享访问申请。传 document_id 或 project_id 之一。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"document_id": map[string]any{"type": "integer", "description": "文档 ID"},
					"project_id":  map[string]any{"type": "integer", "description": "项目 ID"},
				},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "review_access_request",
			Description: "审批分享访问申请（approve/reject）。传 document_id 或 project_id 指明范围。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"request_id":  map[string]any{"type": "integer", "description": "申请 ID"},
					"action":      map[string]any{"type": "string", "description": "approve 或 reject"},
					"document_id": map[string]any{"type": "integer", "description": "文档申请时必填"},
					"project_id":  map[string]any{"type": "integer", "description": "项目申请时必填"},
				},
				"required": []string{"request_id", "action"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "list_skills",
			Description: "列出当前用户已保存的 Agent Skills（name/description）。用户问有哪些 skill、或 /create-skill 前后需要对照时调用。",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "get_skill",
			Description: "按 name 读取当前用户某个 skill 的完整正文（含 description）。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "skill 名称（如 code-review）"},
				},
				"required": []string{"name"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "save_skill",
			Description: "创建或更新当前用户的 Agent Skill（同名则覆盖）。/create-skill 流程的落库步骤；name 小写字母/数字/连字符。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":                     map[string]any{"type": "string", "description": "skill 唯一名，如 weekly-report"},
					"description":              map[string]any{"type": "string", "description": "第三人称：做什么 + 何时用，含触发词"},
					"content":                  map[string]any{"type": "string", "description": "SKILL.md 正文（Markdown 指引）"},
					"disable_model_invocation": map[string]any{"type": "boolean", "description": "true=仅 /name 显式加载（默认）；false=可按 description 自动选用"},
				},
				"required": []string{"name", "description", "content"},
			},
		}},
		{Type: "function", Function: llm.ToolFunc{
			Name:        "delete_skill",
			Description: "删除当前用户的某个 Agent Skill（按 name）。执行前需用户确认。",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "要删除的 skill 名称"},
				},
				"required": []string{"name"},
			},
		}},
	}

	clip := func(s string) string {
		if utf8.RuneCountInString(s) > aiToolMaxResult {
			return string([]rune(s)[:aiToolMaxResult]) + "\n…（内容过长已截断）"
		}
		return s
	}
	// 参数解析失败/执行出错时返回文本，让模型自行调整
	creates := 0 // 本次对话内 create_document 已调用次数
	exec = func(name, argsJSON string) (string, error) {
		var args map[string]any
		if strings.TrimSpace(argsJSON) != "" {
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
				return "", fmt.Errorf("参数不是合法 JSON: %v", err)
			}
		}
		argInt := func(key string, def int64) int64 {
			if v, ok := args[key].(float64); ok && v > 0 {
				return int64(v)
			}
			return def
		}
		argStr := func(key string) string { s, _ := args[key].(string); return strings.TrimSpace(s) }
		// 区分「未传」和 0：归属字段用 0 表示清空。
		argUintOpt := func(key string) (uint, bool, error) {
			v, ok := args[key]
			if !ok || v == nil {
				return 0, false, nil
			}
			f, ok := v.(float64)
			if !ok || f < 0 || f != float64(uint(f)) {
				return 0, true, fmt.Errorf("%s 必须是非负整数", key)
			}
			return uint(f), true, nil
		}
		// 未传 expire_days 保持原值；传 0 清空为永久。
		applyExpire := func(current *time.Time) (*time.Time, error) {
			days, ok, err := argUintOpt("expire_days")
			if err != nil {
				return nil, err
			}
			if !ok {
				return current, nil
			}
			if days == 0 {
				return nil, nil
			}
			t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
			return &t, nil
		}
		reserveCreate := func() error {
			creates++
			if creates > aiMaxCreatesPerReq {
				return fmt.Errorf("单次对话最多创建 %d 篇文档，已达到上限；请先确认已有文档是否满足需求", aiMaxCreatesPerReq)
			}
			var createdToday int64
			now := time.Now()
			startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
			a.DB.Model(&model.Document{}).Where("owner_id = ? AND updated_via = ? AND created_at >= ?", user.ID, "ai", startOfDay).Count(&createdToday)
			if createdToday >= aiMaxCreatesPerDay {
				return fmt.Errorf("你今天通过 AI 创建的文档已达 %d 篇上限，请明天再试或手动创建", aiMaxCreatesPerDay)
			}
			return nil
		}

		switch name {
		case "search_documents":
			q := argStr("query")
			if q == "" {
				return "", fmt.Errorf("query 不能为空")
			}
			limit := argInt("limit", 5)
			if limit > aiToolMaxDocs {
				limit = aiToolMaxDocs
			}
			// 走统一搜索底层（ES 优先、未配置/报错降级 MySQL），与 Web 文档列表同一套逻辑
			p := search.DefaultParams()
			p.Query = q
			p.UserID = user.ID
			p.IsAdmin = user.IsAdmin()
			p.MemberPids = memberPids
			p.Offset = 0
			p.Size = int(limit)
			ids, err := a.searchSvc().Page(p)
			if err != nil {
				return "", fmt.Errorf("搜索失败: %v", err)
			}
			var docs []model.Document
			if len(ids) > 0 {
				// 回表兼底：再套一层权限范围，并按搜索返回的 ID 顺序重排
				a.searchSvc().ScopeQuery(p).Where("id IN ?", ids).
					Select("id, title, slug, version, type, project_id, updated_at").Find(&docs)
				byID := make(map[uint]model.Document, len(docs))
				for _, d := range docs {
					byID[d.ID] = d
				}
				docs = docs[:0]
				for _, id := range ids {
					if d, ok := byID[id]; ok {
						docs = append(docs, d)
					}
				}
			}
			if len(docs) == 0 {
				return "没有找到匹配的文档。", nil
			}
			out := make([]string, 0, len(docs))
			for _, d := range docs {
				out = append(out, fmt.Sprintf("- id=%d [%s] 《%s》{{doc:%d}}（版本 %s）", d.ID, d.Type, d.Title, d.ID, d.Version))
			}
			return clip(strings.Join(out, "\n")), nil

		case "read_document":
			var doc model.Document
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			if err := scope().Where("id = ?", id).First(&doc).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if doc.Type == model.DocTypeHTML {
				return clip(fmt.Sprintf("《%s》是整站文档（正文由多个文件组成，存储于对象存储），请引用链接 {{doc:%d}} 让用户自行查看。", doc.Title, doc.ID)), nil
			}
			runes := []rune(doc.Content)
			total := len(runes)
			if total == 0 {
				return fmt.Sprintf("《%s》（content_version=%d）正文为空。", doc.Title, doc.ContentVersion), nil
			}

			// 分页读取：offset/length 按字符计，默认从 0 读 aiToolMaxResult；返回 content_version 供 update_document 作 base_version
			off := int(argInt("offset", 0))
			if off < 0 || off >= total {
				return "", fmt.Errorf("offset 超出范围（全文共 %d 字符）", total)
			}
			length := int(argInt("length", aiToolMaxResult))
			if length <= 0 || length > aiToolMaxResult {
				length = aiToolMaxResult
			}
			end := off + length
			if end > total {
				end = total
			}
			head := fmt.Sprintf("《%s》（content_version=%d，全文共 %d 字符）第 %d-%d 字符：\n\n", doc.Title, doc.ContentVersion, total, off, end)
			if end < total {
				head += fmt.Sprintf("（未读完，还有 %d 字符，请用 offset=%d 继续读取）\n\n", total-end, end)
			}
			// 按字符预算裁内容：head 占多少就少读多少，避免 clip 截掉正文导致翻页 offset 与实际内容错位
			budget := aiToolMaxResult - utf8.RuneCountInString(head) - 16
			if budget <= 0 {
				return "", fmt.Errorf("分页元数据过长，请稍后重试")
			}
			cend := off + budget
			if cend > end {
				cend = end
			}
			if cend < end {
				head = fmt.Sprintf("《%s》（content_version=%d，全文共 %d 字符）第 %d-%d 字符：\n\n", doc.Title, doc.ContentVersion, total, off, cend)
				head += fmt.Sprintf("（未读完，还有 %d 字符，请用 offset=%d 继续读取）\n\n", total-cend, cend)
			}
			return clip(head + string(runes[off:cend])), nil

		case "list_projects":
			var projects []model.Project
			if err := projectsScope().Order("updated_at desc").Limit(50).Find(&projects).Error; err != nil {
				return "", fmt.Errorf("查询失败: %v", err)
			}
			if len(projects) == 0 {
				return "当前没有可见的项目。", nil
			}
			out := make([]string, 0, len(projects))
			for _, p := range projects {
				out = append(out, fmt.Sprintf("- id=%d %s：%s", p.ID, p.Name, p.Description))
			}
			return clip(strings.Join(out, "\n")), nil

		case "create_project":
			name, desc, errMsg := checkProjectReq(&projectReq{Name: argStr("name"), Description: argStr("description")})
			if errMsg != "" {
				return "", fmt.Errorf("%s", errMsg)
			}
			project := model.Project{Name: name, Description: desc, OwnerID: user.ID}
			if err := a.DB.Create(&project).Error; err != nil {
				return "", fmt.Errorf("创建失败: %v", err)
			}
			return fmt.Sprintf("项目已创建：id=%d %s", project.ID, project.Name), nil

		case "update_project":
			p, err := a.aiManagedProject(user, uint(argInt("id", 0)))
			if err != nil {
				return "", err
			}
			name, desc, errMsg := checkProjectReq(&projectReq{Name: argStr("name"), Description: argStr("description")})
			if errMsg != "" {
				return "", fmt.Errorf("%s", errMsg)
			}
			p.Name = name
			p.Description = desc
			if err := a.DB.Save(p).Error; err != nil {
				return "", fmt.Errorf("更新失败: %v", err)
			}
			return fmt.Sprintf("项目已更新：id=%d %s", p.ID, p.Name), nil

		case "delete_project":
			p, err := a.aiManagedProject(user, uint(argInt("id", 0)))
			if err != nil {
				return "", err
			}
			name := p.Name
			id := p.ID
			if err := a.purgeProject(p); err != nil {
				return "", fmt.Errorf("删除失败: %v", err)
			}
			return fmt.Sprintf("项目已删除：id=%d %s。其下文档已回到未分组。", id, name), nil

		case "list_categories":
			// 与 MCP OpenListCategories 一致：只列密钥/会话用户自己的分类
			var cats []model.Category
			if err := a.DB.Model(&model.Category{}).Where("owner_id = ?", user.ID).
				Order("sort asc, id asc").Limit(100).Find(&cats).Error; err != nil {
				return "", fmt.Errorf("查询失败: %v", err)
			}
			if len(cats) == 0 {
				return "当前没有分类。", nil
			}
			out := make([]string, 0, len(cats))
			for _, c := range cats {
				out = append(out, fmt.Sprintf("- id=%d %s（sort=%d）", c.ID, c.Name, c.Sort))
			}
			return clip(strings.Join(out, "\n")), nil

		case "create_category":
			name, errMsg := checkCategoryReq(argStr("name"))
			if errMsg != "" {
				return "", fmt.Errorf("%s", errMsg)
			}
			sort := 0
			if v, ok, err := argUintOpt("sort"); err != nil {
				return "", err
			} else if ok {
				sort = int(v)
			}
			cat := model.Category{Name: name, Sort: sort, OwnerID: user.ID}
			if err := a.DB.Create(&cat).Error; err != nil {
				return "", fmt.Errorf("创建失败，你的分类名已存在")
			}
			return fmt.Sprintf("分类已创建：id=%d %s", cat.ID, cat.Name), nil

		case "update_category":
			cat, err := a.aiManagedCategory(user, uint(argInt("id", 0)))
			if err != nil {
				return "", err
			}
			name, errMsg := checkCategoryReq(argStr("name"))
			if errMsg != "" {
				return "", fmt.Errorf("%s", errMsg)
			}
			cat.Name = name
			if v, ok, err := argUintOpt("sort"); err != nil {
				return "", err
			} else if ok {
				cat.Sort = int(v)
			}
			if err := a.DB.Save(cat).Error; err != nil {
				return "", fmt.Errorf("更新失败，你的分类名已存在")
			}
			return fmt.Sprintf("分类已更新：id=%d %s（sort=%d）", cat.ID, cat.Name, cat.Sort), nil

		case "delete_category":
			cat, err := a.aiManagedCategory(user, uint(argInt("id", 0)))
			if err != nil {
				return "", err
			}
			name := cat.Name
			id := cat.ID
			if err := a.purgeCategory(cat); err != nil {
				return "", fmt.Errorf("删除失败: %v", err)
			}
			return fmt.Sprintf("分类已删除：id=%d %s。其下文档已变为未分类。", id, name), nil

		case "read_webpage":
			rawURL := argStr("url")
			if rawURL == "" {
				return "", fmt.Errorf("url 不能为空")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			page, err := webfetch.Read(ctx, rawURL)
			cancel()
			if err != nil {
				return "", err
			}
			runes := []rune(page.Text)
			total := len(runes)
			off := 0
			if v, ok, err := argUintOpt("offset"); err != nil {
				return "", err
			} else if ok {
				off = int(v)
			}
			length := aiToolMaxResult
			if v, ok, err := argUintOpt("length"); err != nil {
				return "", err
			} else if ok && v > 0 && int(v) < length {
				length = int(v)
			}
			if off > total {
				off = total
			}
			end := off + length
			if end > total {
				end = total
			}
			head := fmt.Sprintf("网页《%s》%s（全文 %d 字符）第 %d-%d 字符：\n\n", page.Title, page.FinalURL, total, off, end)
			if end < total {
				head += fmt.Sprintf("（未读完，还有 %d 字符，请用 offset=%d 继续读取）\n\n", total-end, end)
			}
			return clip(head + string(runes[off:end])), nil

		case "list_documents":
			limit := argInt("limit", 20)
			if limit > 50 {
				limit = 50
			}
			tx := scope().Select("id, title, slug, version, type, project_id, updated_at")
			if pid := argInt("project_id", 0); pid > 0 {
				tx = tx.Where("project_id = ?", pid)
			}
			if cid := argInt("category_id", 0); cid > 0 {
				tx = tx.Where("category_id = ?", cid)
			}
			var docs []model.Document
			if err := tx.Order("updated_at desc").Limit(int(limit)).Find(&docs).Error; err != nil {
				return "", fmt.Errorf("查询失败: %v", err)
			}
			if len(docs) == 0 {
				return "当前没有可见的文档。", nil
			}
			out := make([]string, 0, len(docs))
			for _, d := range docs {
				out = append(out, fmt.Sprintf("- id=%d [%s] 《%s》{{doc:%d}}（版本 %s）", d.ID, d.Type, d.Title, d.ID, d.Version))
			}
			return clip(strings.Join(out, "\n")), nil

		case "get_share_link":
			var doc model.Document
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			var b strings.Builder
			fmt.Fprintf(&b, "《%s》\n站内链接: {{doc:%d}}", doc.Title, doc.ID)
			var sh model.Share
			if err := a.DB.Where("document_id = ?", doc.ID).First(&sh).Error; err == nil {
				// 分享 token 是属主的管理凭证：仅属主/管理员可见，项目 edit 角色也不暴露
				if !user.IsAdmin() && doc.OwnerID != user.ID {
					b.WriteString("\n该文档已开启分享（详情请联系文档属主）。")
				} else {
					fmt.Fprintf(&b, "\n已开启分享: {{share:%d}}（可编辑: %v", doc.ID, sh.CanEdit)
					if sh.Password != "" {
						b.WriteString("，需密码")
					}
					if sh.ExpireAt != nil {
						fmt.Fprintf(&b, "，%s 过期", sh.ExpireAt.Format("2006-01-02 15:04"))
					}
					b.WriteString("）")
				}
			} else {
				b.WriteString("\n尚未开启分享链接（可在文档页面设置）。")
			}
			return clip(b.String()), nil

		case "revisions_history":
			var doc model.Document
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			var revs []model.DocumentRevision
			if err := a.DB.Where("document_id = ?", doc.ID).
				Select("id", "editor_name", "label", "note", "created_at").
				Order("created_at desc").Limit(20).Find(&revs).Error; err != nil {
				return "", fmt.Errorf("查询失败: %v", err)
			}
			if len(revs) == 0 {
				return "该文档还没有历史修订。", nil
			}
			out := make([]string, 0, len(revs))
			for _, r := range revs {
				tag := r.Label
				if tag == "" {
					tag = "自动快照"
				}
				line := fmt.Sprintf("- revision_id=%d [%s] %s（%s）", r.ID, tag, r.Note, r.CreatedAt.Format("01-02 15:04"))
				if r.EditorName != "" {
					line += fmt.Sprintf(" 编辑: %s", r.EditorName)
				}
				out = append(out, line)
			}
			return clip(strings.Join(out, "\n")), nil

		case "restore_revision":
			id := argInt("id", 0)
			rid := argInt("revision_id", 0)
			if id == 0 || rid == 0 {
				return "", fmt.Errorf("id 和 revision_id 不能为空")
			}
			var doc model.Document
			if err := scope().Where("type <> ?", model.DocTypeHTML).First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			var rev model.DocumentRevision
			if err := a.DB.Where("id = ? AND document_id = ?", rid, doc.ID).First(&rev).Error; err != nil {
				return "", fmt.Errorf("修订不存在（revision_id=%d）", rid)
			}
			if _, err := a.aiApplyDocUpdate(user, &doc, doc.Title, doc.Title, rev.Content, true, nil, nil); err != nil {
				return "", fmt.Errorf("恢复失败: %v", err)
			}
			return clip(fmt.Sprintf("已把《%s》恢复到修订 %d 的内容（当前内容已存为新修订，当前版本 %s）。预览：{{doc:%d}}", doc.Title, rid, doc.Version, doc.ID)), nil

		case "delete_document":
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			var doc model.Document
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			// 软删、停用分享、清访问申请、关掉 is_shared 同一事务；失败则整单回滚。
			// is_shared 必须 Unscoped：软删后默认查询会跳过该行。
			if err := a.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Delete(&model.Document{}, doc.ID).Error; err != nil {
					return err
				}
				if err := tx.Where("document_id = ?", doc.ID).Delete(&model.Share{}).Error; err != nil {
					return err
				}
				if err := tx.Where("document_id = ?", doc.ID).Delete(&model.ShareAccessRequest{}).Error; err != nil {
					return err
				}
				return tx.Unscoped().Model(&model.Document{}).Where("id = ?", doc.ID).Update("is_shared", false).Error
			}); err != nil {
				return "", fmt.Errorf("删除失败: %v", err)
			}
			a.reindexDocAsync(doc.ID) // 软删后重索（带 deleted 标记）
			return fmt.Sprintf("《%s》（id=%d）已移入回收站，可在回收站恢复。", doc.Title, doc.ID), nil

		case "list_trash":
			tx := a.DB.Unscoped().Model(&model.Document{}).Where("deleted_at IS NOT NULL")
			if !user.IsAdmin() {
				tx = tx.Where("owner_id = ?", user.ID)
			}
			var docs []model.Document
			if err := tx.Select("id", "title", "slug", "type", "deleted_at").Order("deleted_at desc").Limit(50).Find(&docs).Error; err != nil {
				return "", fmt.Errorf("查询失败: %v", err)
			}
			if len(docs) == 0 {
				return "回收站是空的。", nil
			}
			out := make([]string, 0, len(docs))
			for _, d := range docs {
				out = append(out, fmt.Sprintf("- id=%d [%s] %s（删除于 %s）", d.ID, d.Type, d.Title, d.DeletedAt.Time.Format("2006-01-02 15:04")))
			}
			return clip(strings.Join(out, "\n")), nil

		case "restore_document":
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			tx := a.DB.Unscoped().Where("deleted_at IS NOT NULL")
			if !user.IsAdmin() {
				tx = tx.Where("owner_id = ?", user.ID)
			}
			var doc model.Document
			if err := tx.First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("回收站中不存在该文档（id=%d）", id)
			}
			var n int64
			a.DB.Model(&model.Document{}).Where("slug = ?", doc.Slug).Count(&n)
			if n > 0 {
				return "", fmt.Errorf("恢复失败：路径 %s 已被其他文档占用，请先处理同名文档", doc.Slug)
			}
			if err := a.DB.Unscoped().Model(&model.Document{}).Where("id = ?", doc.ID).Update("deleted_at", nil).Error; err != nil {
				return "", fmt.Errorf("恢复失败: %v", err)
			}
			a.reindexDocAsync(doc.ID) // 恢复后重索（清除 deleted 标记）
			return clip(fmt.Sprintf("已从回收站恢复《%s》（id=%d）。预览：{{doc:%d}}", doc.Title, doc.ID, doc.ID)), nil

		case "list_templates":
			tx := a.DB.Model(&model.DocTemplate{})
			if !user.IsAdmin() {
				if len(memberPids) == 0 {
					tx = tx.Where("owner_id = ?", user.ID)
				} else {
					tx = tx.Where("owner_id = ? OR (project_id IS NOT NULL AND project_id IN ?)", user.ID, memberPids)
				}
			}
			var tpls []model.DocTemplate
			if err := tx.Select("id", "name", "type", "project_id", "updated_at").Order("updated_at desc").Limit(50).Find(&tpls).Error; err != nil {
				return "", fmt.Errorf("查询失败: %v", err)
			}
			if len(tpls) == 0 {
				return "当前没有可用的模板。", nil
			}
			out := make([]string, 0, len(tpls))
			for _, t := range tpls {
				pid := ""
				if t.ProjectID > 0 {
					pid = fmt.Sprintf("，项目 %d", t.ProjectID)
				}
				out = append(out, fmt.Sprintf("- template_id=%d [%s] %s%s", t.ID, t.Type, t.Name, pid))
			}
			return clip(strings.Join(out, "\n")), nil

		case "get_template":
			tpl, err := a.aiUsableTemplate(user, memberPids, uint(argInt("id", 0)))
			if err != nil {
				return "", err
			}
			return clip(fmt.Sprintf("template_id=%d [%s] %s\nproject_id=%d category_id=%d\n\n%s",
				tpl.ID, tpl.Type, tpl.Name, tpl.ProjectID, tpl.CategoryID, tpl.Content)), nil

		case "create_template":
			name, tplType, errMsg := validateTemplateReq(argStr("name"), argStr("type"), nil)
			if errMsg != "" {
				if argStr("type") == "" {
					name, tplType, errMsg = validateTemplateReq(argStr("name"), model.DocTypeMarkdown, nil)
				}
			}
			if errMsg != "" {
				return "", fmt.Errorf("%s", errMsg)
			}
			content := argStr("content")
			if content != "" {
				c := content
				if _, _, errMsg = validateTemplateReq(name, tplType, &c); errMsg != "" {
					return "", fmt.Errorf("%s", errMsg)
				}
			}
			tpl := model.DocTemplate{Name: name, Type: tplType, Content: content, OwnerID: user.ID}
			if pid, ok, err := argUintOpt("project_id"); err != nil {
				return "", err
			} else if ok {
				if err := a.aiTemplateProjectOK(user, memberPids, pid); err != nil {
					return "", err
				}
				tpl.ProjectID = pid
			}
			if cid, ok, err := argUintOpt("category_id"); err != nil {
				return "", err
			} else if ok {
				if err := a.aiOwnedCategory(user, cid); err != nil {
					return "", err
				}
				tpl.CategoryID = cid
			}
			if err := a.DB.Create(&tpl).Error; err != nil {
				return "", fmt.Errorf("创建失败: %v", err)
			}
			return fmt.Sprintf("模板已创建：template_id=%d [%s] %s", tpl.ID, tpl.Type, tpl.Name), nil

		case "update_template":
			tpl, err := a.aiOwnedTemplate(user, uint(argInt("id", 0)))
			if err != nil {
				return "", err
			}
			name := argStr("name")
			if name == "" {
				name = tpl.Name
			}
			tplType := argStr("type")
			if tplType == "" {
				tplType = tpl.Type
			}
			var contentPtr *string
			if _, has := args["content"]; has {
				c := argStr("content")
				contentPtr = &c
			}
			name, tplType, errMsg := validateTemplateReq(name, tplType, contentPtr)
			if errMsg != "" {
				return "", fmt.Errorf("%s", errMsg)
			}
			tpl.Name, tpl.Type = name, tplType
			if contentPtr != nil {
				tpl.Content = *contentPtr
			}
			if pid, ok, err := argUintOpt("project_id"); err != nil {
				return "", err
			} else if ok {
				if err := a.aiTemplateProjectOK(user, memberPids, pid); err != nil {
					return "", err
				}
				tpl.ProjectID = pid
			}
			if cid, ok, err := argUintOpt("category_id"); err != nil {
				return "", err
			} else if ok {
				if err := a.aiOwnedCategory(user, cid); err != nil {
					return "", err
				}
				tpl.CategoryID = cid
			}
			if err := a.DB.Save(tpl).Error; err != nil {
				return "", fmt.Errorf("更新失败: %v", err)
			}
			return fmt.Sprintf("模板已更新：template_id=%d [%s] %s", tpl.ID, tpl.Type, tpl.Name), nil

		case "delete_template":
			tpl, err := a.aiOwnedTemplate(user, uint(argInt("id", 0)))
			if err != nil {
				return "", err
			}
			name, id := tpl.Name, tpl.ID
			if err := a.DB.Delete(tpl).Error; err != nil {
				return "", fmt.Errorf("删除失败: %v", err)
			}
			return fmt.Sprintf("模板已删除：template_id=%d %s（已创建的文档不受影响）", id, name), nil

		case "apply_template":
			tid := argInt("template_id", 0)
			if tid == 0 {
				return "", fmt.Errorf("template_id 不能为空")
			}
			tpl, err := a.aiUsableTemplate(user, memberPids, uint(tid))
			if err != nil {
				return "", err
			}
			switch tpl.Type {
			case model.DocTypeMarkdown, model.DocTypeMindmap, model.DocTypeBoard, model.DocTypeDrawio:
			default:
				return "", fmt.Errorf("不支持的模板类型：%s", tpl.Type)
			}
			title := argStr("title")
			if title == "" {
				title = tpl.Name
			}
			if err := reserveCreate(); err != nil {
				return "", err
			}
			doc := model.Document{
				Title: title, Version: derivedDocVersion(0),
				Slug: util.RandomSlug(8), Type: tpl.Type,
				Content: tpl.Content, OwnerID: user.ID,
				UpdatedByID: user.ID, UpdatedVia: "ai",
			}
			var dropped []string
			if pid, ok, err := argUintOpt("project_id"); err != nil {
				return "", err
			} else if ok && pid > 0 {
				if err := a.aiOwnedProject(user, pid); err != nil {
					return "", err
				}
				doc.ProjectID = pid
			} else if !ok && tpl.ProjectID > 0 {
				if err := a.aiOwnedProject(user, tpl.ProjectID); err == nil {
					doc.ProjectID = tpl.ProjectID
				} else {
					dropped = append(dropped, "模板绑定的项目不属于你，新文档未分组")
				}
			}
			if cid, ok, err := argUintOpt("category_id"); err != nil {
				return "", err
			} else if ok && cid > 0 {
				if err := a.aiOwnedCategory(user, cid); err != nil {
					return "", err
				}
				doc.CategoryID = cid
			} else if !ok && tpl.CategoryID > 0 {
				if err := a.aiOwnedCategory(user, tpl.CategoryID); err == nil {
					doc.CategoryID = tpl.CategoryID
				} else {
					dropped = append(dropped, "模板绑定的分类不属于你，新文档未分类")
				}
			}
			for i := 0; i < 5; i++ {
				var n int64
				a.DB.Unscoped().Model(&model.Document{}).Where("slug = ?", doc.Slug).Count(&n)
				if n == 0 {
					break
				}
				doc.Slug = util.RandomSlug(8)
			}
			if err := a.DB.Create(&doc).Error; err != nil {
				return "", fmt.Errorf("创建失败: %v", err)
			}
			a.reindexDocAsync(doc.ID)
			msg := fmt.Sprintf("已按模板《%s》创建 [%s] 文档：{{doc:%d}}（id=%d）", tpl.Name, doc.Type, doc.ID, doc.ID)
			if len(dropped) > 0 {
				msg += "\n" + strings.Join(dropped, "\n")
			}
			return clip(msg), nil

		case "set_share":
			// 与页面 UpsertShare 相同的语义；权限：属主或管理员
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			enabled := false
			if v, ok := args["enabled"].(bool); ok {
				enabled = v
			}
			var doc model.Document
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !user.IsAdmin() && doc.OwnerID != user.ID {
				return "", fmt.Errorf("只有文档属主可以管理分享（id=%d）", id)
			}
			var share model.Share
			found := a.DB.Where("document_id = ?", doc.ID).First(&share).Error == nil
			if !enabled {
				if err := a.DB.Transaction(func(tx *gorm.DB) error {
					if found {
						if err := tx.Delete(&share).Error; err != nil {
							return err
						}
						if err := tx.Where("document_id = ?", doc.ID).Delete(&model.ShareAccessRequest{}).Error; err != nil {
							return err
						}
					}
					return tx.Model(&model.Document{}).Where("id = ?", doc.ID).Update("is_shared", false).Error
				}); err != nil {
					return "", fmt.Errorf("关闭分享失败: %v", err)
				}
				a.reindexDocAsync(doc.ID)
				return "分享已关闭。", nil
			}
			if !found {
				share = model.Share{DocumentID: doc.ID, ShareToken: util.RandomHex(16)}
			}
			if ce, ok := args["can_edit"].(bool); ok {
				share.CanEdit = ce
			}
			if rm, ok := args["remove_password"].(bool); ok && rm {
				share.Password = "" // 显式清除已设密码
			}
			if pw := argStr("password"); pw != "" {
				hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
				share.Password = string(hash)
			}
			exp, err := applyExpire(share.ExpireAt)
			if err != nil {
				return "", err
			}
			share.ExpireAt = exp
			if err := a.DB.Transaction(func(tx *gorm.DB) error {
				if err := tx.Save(&share).Error; err != nil {
					return err
				}
				return tx.Model(&model.Document{}).Where("id = ?", doc.ID).Update("is_shared", true).Error
			}); err != nil {
				return "", fmt.Errorf("分享设置失败: %v", err)
			}
			a.reindexDocAsync(doc.ID)
			note := ""
			if share.Password != "" {
				note += "（已设密码）"
			}
			if share.CanEdit {
				note += "（登录用户可编辑）"
			}
			return clip(fmt.Sprintf("分享已开启：{{share:%d}}%s", doc.ID, note)), nil

		case "update_document":
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			newTitle, hasTitle := args["title"]
			newContent, hasContent := args["content"]
			projID, hasProject, err := argUintOpt("project_id")
			if err != nil {
				return "", err
			}
			catID, hasCategory, err := argUintOpt("category_id")
			if err != nil {
				return "", err
			}
			if !hasTitle && !hasContent && !hasProject && !hasCategory {
				return "", fmt.Errorf("title、content、project_id、category_id 至少提供一个")
			}
			var doc model.Document
			if err := scope().Where("type <> ?", model.DocTypeHTML).First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			// 编辑权限：属主 / 管理员 / 项目 edit 角色，与页面编辑一致
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			// 读后写校验：覆盖正文必须携带读取时的 content_version，杜绝模型凭记忆重写丢内容
			if hasContent {
				bv, ok := args["base_version"].(float64)
				if !ok || bv < 0 {
					return "", fmt.Errorf("修改正文必须提供 base_version（来自 read_document 返回的 content_version）；局部修改请改用 replace_section，无需全文")
				}
				if int64(bv) != doc.ContentVersion {
					return "", fmt.Errorf("base_version=%d 与当前 content_version=%d 不一致，文档已被修改过；请重新 read_document 获取最新内容后再更新", int64(bv), doc.ContentVersion)
				}
			}
			oldTitle := doc.Title // 改写前先留档
			newTitleVal := oldTitle
			if hasTitle {
				if t, ok := newTitle.(string); ok && strings.TrimSpace(t) != "" {
					newTitleVal = strings.TrimSpace(t)
					doc.Title = newTitleVal
				}
			}
			hasContentChange := false
			var content string
			if hasContent {
				if s, ok := newContent.(string); ok && strings.TrimSpace(s) != "" {
					if len(s) > aiMaxContentBytes {
						return "", fmt.Errorf("content 超过 %dKB 上限（当前 %dKB），请只提交需要修改的部分或用 replace_section", aiMaxContentBytes>>10, len(s)>>10)
					}
					if err := validateAIDocContent(doc.Type, s); err != nil {
						return "", err
					}
					content = s
					hasContentChange = true
				} else {
					// 传了 content 但为空白：大概率是模型输出被截断，明确报错让它重试，而非静默忽略
					return "", fmt.Errorf("content 为空白或非字符串，疑似输出被截断；请分段调用或改用 replace_section 做局部修改")
				}
			}
			var setProject, setCategory *uint
			if hasProject || hasCategory {
				if doc.OwnerID != user.ID && !user.IsAdmin() {
					return "", fmt.Errorf("只有文档属主可以修改项目或分类归属")
				}
				if hasProject {
					if err := a.aiOwnedProject(user, projID); err != nil {
						return "", err
					}
					setProject = &projID
				}
				if hasCategory {
					if err := a.aiOwnedCategory(user, catID); err != nil {
						return "", err
					}
					setCategory = &catID
				}
			}
			changed, err := a.aiApplyDocUpdate(user, &doc, oldTitle, newTitleVal, content, hasContentChange, setProject, setCategory)
			if err != nil {
				return "", fmt.Errorf("保存失败: %v", err)
			}
			ver := doc.Version
			saved := "已保存。"
			switch {
			case !changed:
				saved = "没有实际变更。"
			case hasContentChange && strings.TrimSpace(content) != "":
				saved = "正文已更新。校验通过：content_version 已递增且正文非空。"
			case hasProject || hasCategory:
				saved = "归属已更新。"
			default:
				saved = "标题已更新。"
			}
			return fmt.Sprintf("文档已保存：{{doc:%d}}（id=%d，版本 %s）%s", doc.ID, doc.ID, ver, saved), nil

		case "replace_section":
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			oldText, hasOld := args["old_text"].(string)
			newText, hasNew := args["new_text"].(string)
			if !hasNew {
				return "", fmt.Errorf("new_text 不能为空（删除内容请显式传空字符串）")
			}
			if !hasOld || strings.TrimSpace(oldText) == "" {
				return "", fmt.Errorf("old_text 不能为空")
			}
			var doc model.Document
			if err := scope().Where("type <> ?", model.DocTypeHTML).First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			if doc.Type != model.DocTypeMarkdown {
				return "", fmt.Errorf("画布文档请用 update_document 整体更新，不要用 replace_section")
			}
			n := strings.Count(doc.Content, oldText)
			if n == 0 {
				return "", fmt.Errorf("old_text 在正文中未找到，请先 read_document 核对原文（注意空格与标点需逐字一致）后再重试")
			}
			occ := int64(0)
			if v, ok := args["occurrence"].(float64); ok && v > 0 {
				occ = int64(v)
			}
			if n > 1 {
				if occ == 0 {
					return "", fmt.Errorf("old_text 匹配到 %d 处，不唯一；请扩写 old_text 包含更多上下文使其唯一，或传 occurrence 指定替换第几处（1-%d）", n, n)
				}
				if occ > int64(n) {
					return "", fmt.Errorf("old_text 匹配到 %d 处，occurrence=%d 超出范围", n, occ)
				}
			}
			if n == 1 {
				occ = 1
			}
			// 定位第 occ 处匹配并替换（occ 已校验在 1..n 范围内）
			start := -1
			for i := int64(0); i < occ; i++ {
				next := strings.Index(doc.Content[start+1:], oldText)
				if next < 0 { // 理论上不可达（n 已验证），防御性兜底
					return "", fmt.Errorf("old_text 定位失败，请重试")
				}
				start += 1 + next
			}
			newContent := doc.Content[:start] + newText + doc.Content[start+len(oldText):]
			if newContent == doc.Content {
				return "old_text 与 new_text 相同，正文未变化。", nil
			}
			if _, err := a.aiApplyDocUpdate(user, &doc, doc.Title, doc.Title, newContent, true, nil, nil); err != nil {
				return "", fmt.Errorf("保存失败: %v", err)
			}
			check := verifyReplaceResult(newContent, oldText, newText)
			return fmt.Sprintf("已完成局部替换（id=%d，版本 %s）。%s 预览：{{doc:%d}}", doc.ID, doc.Version, check, doc.ID), nil

		case "create_document":
			if err := reserveCreate(); err != nil {
				return "", err
			}
			title := argStr("title")
			if title == "" {
				return "", fmt.Errorf("title 不能为空")
			}
			docType := argStr("type")
			if docType == "" {
				docType = model.DocTypeMarkdown
			}
			switch docType {
			case model.DocTypeMarkdown, model.DocTypeMindmap, model.DocTypeBoard, model.DocTypeDrawio:
			case model.DocTypeHTML:
				return "", fmt.Errorf("html 整站请走上传接口，智能体不支持创建")
			default:
				return "", fmt.Errorf("不支持的文档类型：%s", docType)
			}
			contentAI := argStr("content")
			if len(contentAI) > aiMaxContentBytes {
				return "", fmt.Errorf("content 超过 %dKB 上限（当前 %dKB），请精简后重试", aiMaxContentBytes>>10, len(contentAI)>>10)
			}
			doc := model.Document{
				Title: title, Version: derivedDocVersion(0),
				Slug: util.RandomSlug(8), Type: docType,
				Content: contentAI, OwnerID: user.ID,
				UpdatedByID: user.ID, UpdatedVia: "ai",
			}
			if pid, ok, err := argUintOpt("project_id"); err != nil {
				return "", err
			} else if ok && pid > 0 {
				if err := a.aiOwnedProject(user, pid); err != nil {
					return "", err
				}
				doc.ProjectID = pid
			}
			if cid, ok, err := argUintOpt("category_id"); err != nil {
				return "", err
			} else if ok && cid > 0 {
				if err := a.aiOwnedCategory(user, cid); err != nil {
					return "", err
				}
				doc.CategoryID = cid
			}
			for i := 0; i < 5; i++ {
				var n int64
				a.DB.Unscoped().Model(&model.Document{}).Where("slug = ?", doc.Slug).Count(&n)
				if n == 0 {
					break
				}
				doc.Slug = util.RandomSlug(8)
			}
			if err := a.DB.Create(&doc).Error; err != nil {
				return "", fmt.Errorf("创建失败: %v", err)
			}
			a.reindexDocAsync(doc.ID)
			return fmt.Sprintf("文档已创建：{{doc:%d}}（id=%d，type=%s）", doc.ID, doc.ID, doc.Type), nil

		case "list_skills":
			var skills []model.ChatSkill
			a.DB.Select("id", "name", "description", "disable_model_invocation", "updated_at").
				Where("user_id = ?", user.ID).Order("name asc").Limit(200).Find(&skills)
			if len(skills) == 0 {
				return "暂无自定义 skill。可用 /create-skill 创建。", nil
			}
			lines := make([]string, 0, len(skills))
			for _, s := range skills {
				mode := "仅 /" + s.Name + " 显式调用"
				if !s.DisableModelInvocation {
					mode = "可自动选用"
				}
				lines = append(lines, fmt.Sprintf("- /%s：%s（%s）", s.Name, s.Description, mode))
			}
			return clip(strings.Join(lines, "\n")), nil

		case "get_skill":
			name := strings.ToLower(argStr("name"))
			if name == "" {
				return "", fmt.Errorf("name 不能为空")
			}
			var sk model.ChatSkill
			if err := a.DB.Where("user_id = ? AND name = ?", user.ID, name).First(&sk).Error; err != nil {
				return "", fmt.Errorf("skill 不存在：/%s", name)
			}
			return clip(fmt.Sprintf("name: %s\ndescription: %s\ndisable_model_invocation: %v\n\n%s",
				sk.Name, sk.Description, sk.DisableModelInvocation, sk.Content)), nil

		case "save_skill":
			name := strings.ToLower(argStr("name"))
			desc := argStr("description")
			body := argStr("content")
			if err := validateSkillFields(name, desc, body); err != nil {
				return "", err
			}
			if isCreateSkillCommand(name) {
				return "", fmt.Errorf("name 与内置指令 /create-skill 冲突，请换一个名称")
			}
			disable := true
			if v, ok := args["disable_model_invocation"].(bool); ok {
				disable = v
			}
			var existing model.ChatSkill
			err := a.DB.Where("user_id = ? AND name = ?", user.ID, name).First(&existing).Error
			if err != nil && a.skillCount(user.ID) >= skillMaxPerUser {
				return "", fmt.Errorf("最多保存 %d 个 skill，请先删除不用的再创建", skillMaxPerUser)
			}
			if err == nil {
				if err := a.DB.Model(&existing).Updates(map[string]any{
					"description":              desc,
					"content":                  body,
					"disable_model_invocation": disable,
				}).Error; err != nil {
					return "", fmt.Errorf("更新失败: %v", err)
				}
				return fmt.Sprintf("已更新 skill：/%s（id=%d）。用户可输入 /%s 调用。", name, existing.ID, name), nil
			}
			sk := model.ChatSkill{
				UserID: user.ID, Name: name, Description: desc, Content: body,
				DisableModelInvocation: disable,
			}
			if err := a.DB.Create(&sk).Error; err != nil {
				if isDupKey(err) {
					return "", fmt.Errorf("同名 skill 已存在，请改名或先 get_skill 再 save_skill 覆盖")
				}
				return "", fmt.Errorf("保存失败: %v", err)
			}
			return fmt.Sprintf("已创建 skill：/%s（id=%d）。用户可输入 /%s 调用。", name, sk.ID, name), nil

		case "delete_skill":
			name := strings.ToLower(argStr("name"))
			if name == "" {
				return "", fmt.Errorf("name 不能为空")
			}
			res := a.DB.Where("user_id = ? AND name = ?", user.ID, name).Delete(&model.ChatSkill{})
			if res.Error != nil {
				return "", fmt.Errorf("删除失败: %v", res.Error)
			}
			if res.RowsAffected == 0 {
				return "", fmt.Errorf("skill 不存在：/%s", name)
			}
			return fmt.Sprintf("已删除 skill：/%s", name), nil

		case "duplicate_document":
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			var doc model.Document
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			if doc.Type == model.DocTypeHTML {
				return "", fmt.Errorf("HTML 整站请在页面上复制；智能体通道暂不复制站点文件")
			}
			if err := reserveCreate(); err != nil {
				return "", err
			}
			newDoc, err := a.aiDuplicateDoc(user, &doc)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("已复制为：{{doc:%d}}（id=%d，《%s》）", newDoc.ID, newDoc.ID, newDoc.Title), nil

		case "purge_document":
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			tx := a.DB.Unscoped().Where("deleted_at IS NOT NULL")
			if !user.IsAdmin() {
				tx = tx.Where("owner_id = ?", user.ID)
			}
			var doc model.Document
			if err := tx.First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("回收站中不存在该文档（id=%d）", id)
			}
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			title := doc.Title
			if err := a.purgeDoc(&doc); err != nil {
				return "", fmt.Errorf("彻底删除失败: %v", err)
			}
			return fmt.Sprintf("已彻底删除《%s》（id=%d），不可恢复。", title, id), nil

		case "label_revision":
			id := argInt("id", 0)
			rid := argInt("revision_id", 0)
			if id == 0 || rid == 0 {
				return "", fmt.Errorf("id 和 revision_id 不能为空")
			}
			var doc model.Document
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			var rev model.DocumentRevision
			if err := a.DB.Where("id = ? AND document_id = ?", rid, doc.ID).First(&rev).Error; err != nil {
				return "", fmt.Errorf("修订不存在（revision_id=%d）", rid)
			}
			label := argStr("label")
			note := argStr("note")
			if utf8.RuneCountInString(label) > 32 || utf8.RuneCountInString(note) > 500 {
				return "", fmt.Errorf("label 最多 32 字，note 最多 500 字")
			}
			if label != "" {
				var dup int64
				a.DB.Model(&model.DocumentRevision{}).
					Where("document_id = ? AND label = ? AND id <> ?", doc.ID, label, rev.ID).Count(&dup)
				if dup > 0 {
					return "", fmt.Errorf("版本号已被其他历史版本使用")
				}
			}
			if err := a.DB.Model(&rev).Updates(map[string]any{"label": label, "note": note}).Error; err != nil {
				return "", fmt.Errorf("保存失败: %v", err)
			}
			if label == "" {
				return fmt.Sprintf("已取消修订 %d 的版本标签。", rid), nil
			}
			return fmt.Sprintf("已给修订 %d 打标签：%s", rid, label), nil

		case "list_comments":
			id := argInt("id", 0)
			if id == 0 {
				return "", fmt.Errorf("id 不能为空")
			}
			var doc model.Document
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			views := a.listCommentViews(doc.ID)
			if len(views) == 0 {
				return "该文档暂无评论。", nil
			}
			out := make([]string, 0, len(views))
			for _, v := range views {
				name := v.Name
				if name == "" {
					name = "游客"
				}
				line := fmt.Sprintf("- comment_id=%d %s：%s（%s）", v.ID, name, v.Content, v.CreatedAt.Format("01-02 15:04"))
				if v.ParentID > 0 {
					line += fmt.Sprintf(" ←回复 %d", v.ParentID)
				}
				out = append(out, line)
			}
			return clip(strings.Join(out, "\n")), nil

		case "add_comment":
			id := argInt("id", 0)
			content := argStr("content")
			if id == 0 || content == "" {
				return "", fmt.Errorf("id 与 content 不能为空")
			}
			if utf8.RuneCountInString(content) > commentMaxRunes {
				return "", fmt.Errorf("评论最多 %d 字", commentMaxRunes)
			}
			var doc model.Document
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !canEditDoc(&doc) {
				return "", fmt.Errorf("没有该文档的编辑权限（id=%d）", id)
			}
			parentID := uint(argInt("parent_id", 0))
			var parent *model.Comment
			if parentID > 0 {
				var p model.Comment
				if err := a.DB.Where("id = ? AND document_id = ? AND parent_id = 0", parentID, doc.ID).First(&p).Error; err != nil {
					return "", fmt.Errorf("只能回复顶层评论（parent_id=%d）", parentID)
				}
				if p.UserID == user.ID {
					return "", fmt.Errorf("不能回复自己的评论")
				}
				parent = &p
			}
			cm := model.Comment{DocumentID: doc.ID, ParentID: parentID, UserID: user.ID, Content: content}
			if err := a.DB.Create(&cm).Error; err != nil {
				return "", fmt.Errorf("发表失败: %v", err)
			}
			a.notifyNewComment(&doc, &cm, user.DisplayName(), parent)
			return fmt.Sprintf("评论已发表：comment_id=%d（文档 {{doc:%d}}）", cm.ID, doc.ID), nil

		case "delete_comment":
			id := argInt("id", 0)
			cid := argInt("comment_id", 0)
			if id == 0 || cid == 0 {
				return "", fmt.Errorf("id 与 comment_id 不能为空")
			}
			var doc model.Document
			if err := scope().First(&doc, id).Error; err != nil {
				return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
			}
			if !user.IsAdmin() && doc.OwnerID != user.ID {
				return "", fmt.Errorf("只有文档属主可以删除评论")
			}
			err := a.DB.Transaction(func(tx *gorm.DB) error {
				res := tx.Where("id = ? AND document_id = ?", cid, doc.ID).Delete(&model.Comment{})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					return fmt.Errorf("评论不存在（comment_id=%d）", cid)
				}
				return tx.Where("parent_id = ?", cid).Delete(&model.Comment{}).Error
			})
			if err != nil {
				if strings.Contains(err.Error(), "评论不存在") {
					return "", err
				}
				return "", fmt.Errorf("删除失败: %v", err)
			}
			return fmt.Sprintf("已删除评论 %d 及其回复。", cid), nil

		case "list_project_members":
			p, err := a.aiVisibleProject(user, memberPids, uint(argInt("project_id", 0)))
			if err != nil {
				return "", err
			}
			var owner model.User
			a.DB.First(&owner, p.OwnerID)
			lines := []string{fmt.Sprintf("- user_id=%d %s（owner）", owner.ID, owner.DisplayName())}
			var ms []model.ProjectMember
			a.DB.Where("project_id = ? AND user_id <> ?", p.ID, p.OwnerID).Order("created_at asc").Limit(100).Find(&ms)
			uids := make([]uint, 0, len(ms))
			for _, m := range ms {
				uids = append(uids, m.UserID)
			}
			umap := map[uint]model.User{}
			if len(uids) > 0 {
				var us []model.User
				a.DB.Where("id IN ?", uids).Find(&us)
				for _, u := range us {
					umap[u.ID] = u
				}
			}
			for _, m := range ms {
				u := umap[m.UserID]
				name := (&u).DisplayName()
				if name == "" {
					name = u.Username
				}
				lines = append(lines, fmt.Sprintf("- user_id=%d %s（%s）", m.UserID, name, m.Role))
			}
			return clip(strings.Join(lines, "\n")), nil

		case "add_project_member":
			p, err := a.aiManagedProject(user, uint(argInt("project_id", 0)))
			if err != nil {
				return "", err
			}
			role := argStr("role")
			if role == "" {
				role = model.MemberRoleView
			}
			if role != model.MemberRoleView && role != model.MemberRoleEdit {
				return "", fmt.Errorf("角色非法，仅支持 view / edit")
			}
			target, err := a.aiResolveUser(argStr("username"), uint(argInt("user_id", 0)))
			if err != nil {
				return "", err
			}
			if target.ID == p.OwnerID {
				return "", fmt.Errorf("项目所有者无需添加为成员")
			}
			m := model.ProjectMember{ProjectID: p.ID, UserID: target.ID, Role: role}
			if err := a.DB.Create(&m).Error; err != nil {
				if isDupKey(err) {
					return "", fmt.Errorf("该用户已是项目成员")
				}
				return "", fmt.Errorf("添加失败: %v", err)
			}
			a.notifyProjectMemberAdded(p, m.UserID, m.Role)
			return fmt.Sprintf("已添加成员 user_id=%d %s（%s）到项目 id=%d", target.ID, target.DisplayName(), role, p.ID), nil

		case "update_project_member":
			p, err := a.aiManagedProject(user, uint(argInt("project_id", 0)))
			if err != nil {
				return "", err
			}
			uid := uint(argInt("user_id", 0))
			role := argStr("role")
			if uid == 0 || (role != model.MemberRoleView && role != model.MemberRoleEdit) {
				return "", fmt.Errorf("user_id 与 role（view/edit）必填")
			}
			var m model.ProjectMember
			if err := a.DB.Where("project_id = ? AND user_id = ?", p.ID, uid).First(&m).Error; err != nil {
				return "", fmt.Errorf("成员不存在（user_id=%d）", uid)
			}
			if err := a.DB.Model(&m).Update("role", role).Error; err != nil {
				return "", fmt.Errorf("更新失败: %v", err)
			}
			a.notifyProjectMemberRole(p, m.UserID, role)
			return fmt.Sprintf("已将 user_id=%d 角色改为 %s", uid, role), nil

		case "remove_project_member":
			p, err := a.aiManagedProject(user, uint(argInt("project_id", 0)))
			if err != nil {
				return "", err
			}
			uid := uint(argInt("user_id", 0))
			if uid == 0 {
				return "", fmt.Errorf("user_id 不能为空")
			}
			var m model.ProjectMember
			if err := a.DB.Where("project_id = ? AND user_id = ?", p.ID, uid).First(&m).Error; err != nil {
				return "", fmt.Errorf("成员不存在（user_id=%d）", uid)
			}
			if err := a.DB.Delete(&m).Error; err != nil {
				return "", fmt.Errorf("移除失败: %v", err)
			}
			a.notifyProjectMemberRemoved(p, uid)
			return fmt.Sprintf("已从项目 id=%d 移除 user_id=%d", p.ID, uid), nil

		case "get_project_share":
			p, err := a.aiManagedProject(user, uint(argInt("project_id", 0)))
			if err != nil {
				return "", err
			}
			var ps model.ProjectShare
			if a.DB.Where("project_id = ?", p.ID).First(&ps).Error != nil {
				return fmt.Sprintf("项目 id=%d 尚未开启分享。", p.ID), nil
			}
			note := ""
			if ps.CanEdit {
				note += "，登录用户可编辑"
			}
			if ps.HasPassword() {
				note += "，需密码"
			}
			if ps.ExpireAt != nil {
				note += "，" + ps.ExpireAt.Format("2006-01-02 15:04") + " 过期"
			}
			return fmt.Sprintf("项目分享已开启：{{pshare:%d}}%s", p.ID, note), nil

		case "set_project_share":
			p, err := a.aiManagedProject(user, uint(argInt("project_id", 0)))
			if err != nil {
				return "", err
			}
			enabled := false
			if v, ok := args["enabled"].(bool); ok {
				enabled = v
			}
			var ps model.ProjectShare
			found := a.DB.Where("project_id = ?", p.ID).First(&ps).Error == nil
			if !enabled {
				if found {
					if err := a.DB.Transaction(func(tx *gorm.DB) error {
						if err := tx.Delete(&ps).Error; err != nil {
							return err
						}
						return tx.Where("project_id = ?", p.ID).Delete(&model.ShareAccessRequest{}).Error
					}); err != nil {
						return "", fmt.Errorf("关闭分享失败: %v", err)
					}
				}
				return fmt.Sprintf("已关闭项目 id=%d 的分享。", p.ID), nil
			}
			if !found {
				ps = model.ProjectShare{ProjectID: p.ID, ShareToken: util.RandomHex(16)}
			}
			if v, ok := args["can_edit"].(bool); ok {
				ps.CanEdit = v
			}
			if pwd := argStr("password"); pwd != "" {
				hash, _ := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
				ps.Password = string(hash)
			} else if v, ok := args["remove_password"].(bool); ok && v {
				ps.Password = ""
			} else if !found {
				ps.Password = ""
			}
			exp, err := applyExpire(ps.ExpireAt)
			if err != nil {
				return "", err
			}
			ps.ExpireAt = exp
			if err := a.DB.Save(&ps).Error; err != nil {
				return "", fmt.Errorf("分享设置失败: %v", err)
			}
			note := ""
			if ps.CanEdit {
				note += "（登录用户可编辑）"
			}
			return fmt.Sprintf("项目分享已开启：{{pshare:%d}}%s", p.ID, note), nil

		case "list_access_requests":
			docID := uint(argInt("document_id", 0))
			projID := uint(argInt("project_id", 0))
			if (docID == 0 && projID == 0) || (docID > 0 && projID > 0) {
				return "", fmt.Errorf("请只传 document_id 或 project_id 之一")
			}
			var items []model.ShareAccessRequest
			if docID > 0 {
				var doc model.Document
				if err := scope().First(&doc, docID).Error; err != nil {
					return "", fmt.Errorf("文档不存在或无权访问（id=%d）", docID)
				}
				if !user.IsAdmin() && doc.OwnerID != user.ID {
					return "", fmt.Errorf("只有文档属主可以查看访问申请")
				}
				a.DB.Where("document_id = ?", docID).Order("status asc, id desc").Limit(50).Find(&items)
			} else {
				if _, err := a.aiManagedProject(user, projID); err != nil {
					return "", err
				}
				a.DB.Where("project_id = ?", projID).Order("status asc, id desc").Limit(50).Find(&items)
			}
			if len(items) == 0 {
				return "暂无访问申请。", nil
			}
			statusName := map[int]string{
				model.AccessPending: "pending", model.AccessApproved: "approved", model.AccessRejected: "rejected",
			}
			out := make([]string, 0, len(items))
			for _, r := range items {
				out = append(out, fmt.Sprintf("- request_id=%d [%s] %s contact=%s msg=%s（%s）",
					r.ID, statusName[r.Status], r.GuestName, r.GuestContact, r.Message, r.CreatedAt.Format("01-02 15:04")))
			}
			return clip(strings.Join(out, "\n")), nil

		case "review_access_request":
			rid := uint(argInt("request_id", 0))
			action := argStr("action")
			docID := uint(argInt("document_id", 0))
			projID := uint(argInt("project_id", 0))
			if rid == 0 || (action != "approve" && action != "reject") {
				return "", fmt.Errorf("request_id 与 action（approve/reject）必填")
			}
			if (docID == 0 && projID == 0) || (docID > 0 && projID > 0) {
				return "", fmt.Errorf("请只传 document_id 或 project_id 之一")
			}
			status := model.AccessRejected
			if action == "approve" {
				status = model.AccessApproved
			}
			var req model.ShareAccessRequest
			var doc model.Document
			var project *model.Project
			if docID > 0 {
				if err := scope().First(&doc, docID).Error; err != nil {
					return "", fmt.Errorf("文档不存在或无权访问（id=%d）", docID)
				}
				if !user.IsAdmin() && doc.OwnerID != user.ID {
					return "", fmt.Errorf("只有文档属主可以审批")
				}
				if err := a.DB.Where("id = ? AND document_id = ?", rid, docID).First(&req).Error; err != nil {
					return "", fmt.Errorf("申请不存在")
				}
			} else {
				p, err := a.aiManagedProject(user, projID)
				if err != nil {
					return "", err
				}
				project = p
				if err := a.DB.Where("id = ? AND project_id = ?", rid, projID).First(&req).Error; err != nil {
					return "", fmt.Errorf("申请不存在")
				}
			}
			if req.Status != model.AccessPending {
				return "", fmt.Errorf("该申请已处理过")
			}
			now := time.Now()
			if err := a.DB.Model(&req).Updates(map[string]any{
				"status": status, "reviewed_at": now, "reviewer_id": user.ID,
			}).Error; err != nil {
				return "", fmt.Errorf("审批失败: %v", err)
			}
			req.Status = status
			if docID > 0 {
				var share model.Share
				token := ""
				if a.DB.Where("document_id = ?", doc.ID).First(&share).Error == nil {
					token = share.ShareToken
				}
				a.notifyAccessReviewed(&req, &doc, action == "approve", token)
			} else {
				var ps model.ProjectShare
				token := ""
				if a.DB.Where("project_id = ?", project.ID).First(&ps).Error == nil {
					token = ps.ShareToken
				}
				a.notifyProjectAccessReviewed(&req, project, action == "approve", token)
			}
			return fmt.Sprintf("已%s申请 request_id=%d", map[string]string{"approve": "通过", "reject": "拒绝"}[action], rid), nil
		}
		return "", fmt.Errorf("未知工具: %s", name)
	}

	// 服务端预读专用：一次拼齐全量/相关片段，不进模型工具列表，也不经 exec。
	injectRead = func(id int64, maxChars int, query string) (string, error) {
		if id <= 0 {
			return "", fmt.Errorf("id 不能为空")
		}
		if maxChars <= 0 || maxChars > aiPrereadMaxChars {
			maxChars = aiPrereadMaxChars
		}
		var doc model.Document
		if err := scope().Where("id = ?", id).First(&doc).Error; err != nil {
			return "", fmt.Errorf("文档不存在或无权访问（id=%d）", id)
		}
		if doc.Type == model.DocTypeHTML {
			return fmt.Sprintf("《%s》是整站文档（正文由多个文件组成，存储于对象存储），请引用链接 {{doc:%d}} 让用户自行查看。", doc.Title, doc.ID), nil
		}
		return buildDocInjectBody(doc.Title, doc.ContentVersion, doc.Content, query, maxChars), nil
	}
	return defs, exec, injectRead
}

// aiApplyDocUpdate AI 通道的文档保存：与 UpdateDoc 相同的乐观锁 + 修订快照 + 版本派生，
// updated_via 记为 "ai" 以便审计区分。oldTitle 为加载时的标题，newTitle 为期望的新标题。
// 返回 changed 表示是否有实际变更落库（false = 内容相同且标题未变，调用方应如实告知模型）。
func (a *App) aiApplyDocUpdate(user *model.User, doc *model.Document, oldTitle, newTitle, newContent string, hasContent bool, projectID, categoryID *uint) (bool, error) {
	changed := false
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		var fresh model.Document
		if err := tx.Select("id", "content", "content_version", "project_id", "category_id").First(&fresh, doc.ID).Error; err != nil {
			return err
		}
		updates := map[string]any{"updated_by_id": user.ID, "updated_via": "ai"}
		if newTitle != oldTitle {
			updates["title"] = newTitle
		}
		if projectID != nil && *projectID != fresh.ProjectID {
			updates["project_id"] = *projectID
			doc.ProjectID = *projectID
		}
		if categoryID != nil && *categoryID != fresh.CategoryID {
			updates["category_id"] = *categoryID
			doc.CategoryID = *categoryID
		}
		if hasContent && newContent != fresh.Content {
			updates["content"] = newContent
			updates["content_version"] = fresh.ContentVersion + 1
			updates["version"] = derivedDocVersion(fresh.ContentVersion + 1)
		}
		if len(updates) == 2 { // 只有 updated_by/updated_via：无实际变更
			return nil
		}
		changed = true
		q := tx.Model(&model.Document{}).Where("id = ?", doc.ID)
		if hasContent {
			q = q.Where("content_version = ?", fresh.ContentVersion)
		}
		res := q.Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if hasContent && res.RowsAffected == 0 {
			changed = false
			return fmt.Errorf("内容刚被其他人修改（并发冲突），请重新读取后再试")
		}
		if hasContent && newContent != fresh.Content {
			if err := tx.Create(&model.DocumentRevision{
				DocumentID: doc.ID, EditorID: user.ID, EditorName: user.DisplayName(), Content: fresh.Content,
			}).Error; err != nil {
				return err
			}
			if err := pruneRevisions(tx, doc.ID); err != nil {
				return err
			}
			// 回写新版本号，调用方返回给模型的信息才是落库后的真实版本
			doc.ContentVersion = fresh.ContentVersion + 1
			doc.Version = derivedDocVersion(fresh.ContentVersion + 1)
		}
		return nil
	})
	if err == nil {
		a.reindexDocAsync(doc.ID) // AI 通道内容/标题变更后写时双写
	}
	return changed, err
}

func (a *App) aiManagedProject(user *model.User, id uint) (*model.Project, error) {
	if id == 0 {
		return nil, fmt.Errorf("id 不能为空")
	}
	var p model.Project
	if err := a.DB.First(&p, id).Error; err != nil {
		return nil, fmt.Errorf("项目不存在（id=%d）", id)
	}
	if !user.IsAdmin() && p.OwnerID != user.ID {
		return nil, fmt.Errorf("无权操作他人的项目（id=%d）", id)
	}
	return &p, nil
}

func (a *App) aiManagedCategory(user *model.User, id uint) (*model.Category, error) {
	if id == 0 {
		return nil, fmt.Errorf("id 不能为空")
	}
	var c model.Category
	if err := a.DB.First(&c, id).Error; err != nil {
		return nil, fmt.Errorf("分类不存在（id=%d）", id)
	}
	if !user.IsAdmin() && c.OwnerID != user.ID {
		return nil, fmt.Errorf("无权操作他人的分类（id=%d）", id)
	}
	return &c, nil
}

func (a *App) aiOwnedProject(user *model.User, id uint) error {
	if id == 0 {
		return nil
	}
	var p model.Project
	if err := a.DB.First(&p, id).Error; err != nil || p.OwnerID != user.ID {
		return fmt.Errorf("项目不存在或不属于你（project_id=%d）", id)
	}
	return nil
}

func (a *App) aiOwnedCategory(user *model.User, id uint) error {
	if id == 0 {
		return nil
	}
	var c model.Category
	if err := a.DB.First(&c, id).Error; err != nil || c.OwnerID != user.ID {
		return fmt.Errorf("分类不存在或不属于你（category_id=%d）", id)
	}
	return nil
}

func (a *App) aiVisibleProject(user *model.User, memberPids []uint, id uint) (*model.Project, error) {
	if id == 0 {
		return nil, fmt.Errorf("project_id 不能为空")
	}
	var p model.Project
	if err := a.DB.First(&p, id).Error; err != nil {
		return nil, fmt.Errorf("项目不存在（id=%d）", id)
	}
	if user.IsAdmin() || p.OwnerID == user.ID {
		return &p, nil
	}
	for _, pid := range memberPids {
		if pid == p.ID {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("无权查看该项目（id=%d）", id)
}

func (a *App) aiUsableTemplate(user *model.User, memberPids []uint, id uint) (*model.DocTemplate, error) {
	if id == 0 {
		return nil, fmt.Errorf("id 不能为空")
	}
	var tpl model.DocTemplate
	if err := a.DB.First(&tpl, id).Error; err != nil {
		return nil, fmt.Errorf("模板不存在（id=%d）", id)
	}
	if user.IsAdmin() || tpl.OwnerID == user.ID {
		return &tpl, nil
	}
	if tpl.ProjectID == 0 {
		return nil, fmt.Errorf("模板不可用（id=%d）", id)
	}
	for _, pid := range memberPids {
		if pid == tpl.ProjectID {
			return &tpl, nil
		}
	}
	return nil, fmt.Errorf("模板不可用（id=%d）", id)
}

func (a *App) aiOwnedTemplate(user *model.User, id uint) (*model.DocTemplate, error) {
	if id == 0 {
		return nil, fmt.Errorf("id 不能为空")
	}
	var tpl model.DocTemplate
	if err := a.DB.First(&tpl, id).Error; err != nil {
		return nil, fmt.Errorf("模板不存在（id=%d）", id)
	}
	if !user.IsAdmin() && tpl.OwnerID != user.ID {
		return nil, fmt.Errorf("无权操作他人的模板（id=%d）", id)
	}
	return &tpl, nil
}

func (a *App) aiTemplateProjectOK(user *model.User, memberPids []uint, projectID uint) error {
	if projectID == 0 {
		return nil
	}
	var p model.Project
	if err := a.DB.First(&p, projectID).Error; err != nil {
		return fmt.Errorf("项目不存在（project_id=%d）", projectID)
	}
	if p.OwnerID == user.ID {
		return nil
	}
	for _, pid := range memberPids {
		if pid == projectID {
			return nil
		}
	}
	return fmt.Errorf("只能绑定自己拥有或参与的项目（project_id=%d）", projectID)
}

func (a *App) aiResolveUser(username string, userID uint) (*model.User, error) {
	var u model.User
	switch {
	case username != "":
		if err := a.DB.Where("username = ? AND status = ?", username, model.StatusEnabled).First(&u).Error; err != nil {
			return nil, fmt.Errorf("用户不存在或已禁用：%s", username)
		}
	case userID > 0:
		if err := a.DB.Where("id = ? AND status = ?", userID, model.StatusEnabled).First(&u).Error; err != nil {
			return nil, fmt.Errorf("用户不存在或已禁用（user_id=%d）", userID)
		}
	default:
		return nil, fmt.Errorf("请提供 username 或 user_id")
	}
	return &u, nil
}

func (a *App) aiDuplicateDoc(user *model.User, doc *model.Document) (*model.Document, error) {
	newProjectID := doc.ProjectID
	if newProjectID != 0 {
		var proj model.Project
		if a.DB.First(&proj, newProjectID).Error != nil {
			newProjectID = 0
		} else if proj.OwnerID != user.ID {
			var n int64
			a.DB.Model(&model.ProjectMember{}).Where("project_id = ? AND user_id = ?", newProjectID, user.ID).Count(&n)
			if n == 0 {
				newProjectID = 0
			}
		}
	}
	newCategoryID := doc.CategoryID
	if newCategoryID != 0 {
		var cat model.Category
		if a.DB.First(&cat, newCategoryID).Error != nil || cat.OwnerID != user.ID {
			newCategoryID = 0
		}
	}
	r := []rune(doc.Title)
	if len(r) > 250 {
		r = r[:250]
	}
	newDoc := model.Document{
		Title: string(r) + "（副本）", Version: derivedDocVersion(0),
		Slug: util.RandomSlug(8), Type: doc.Type, Content: doc.Content,
		OwnerID: user.ID, UpdatedByID: user.ID, UpdatedVia: "ai",
		ProjectID: newProjectID, CategoryID: newCategoryID,
	}
	for i := 0; i < 5; i++ {
		var n int64
		a.DB.Unscoped().Model(&model.Document{}).Where("slug = ?", newDoc.Slug).Count(&n)
		if n == 0 {
			break
		}
		newDoc.Slug = util.RandomSlug(8)
	}
	if err := a.DB.Create(&newDoc).Error; err != nil {
		return nil, fmt.Errorf("复制失败: %v", err)
	}
	a.reindexDocAsync(newDoc.ID)
	return &newDoc, nil
}
