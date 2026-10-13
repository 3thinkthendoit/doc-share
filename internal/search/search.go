// Package search 提供文档搜索的唯一底层实现，被 Web 文档列表页、AI 智能体工具
// (search_documents) 与 MCP/OpenAPI 文档搜索共用，确保三处「搜到同一批文档」。
//
// 设计要点（第一性）：
//   - MySQL 是访问控制的真相源，ES 只是文本检索加速器。因此本包对外只产出
//     「有序的文档 ID + 总数」，调用方拿到 ID 后必须回表并再套一次 ScopeQuery
//     做权限兜底（见 handler 的 hydration），即使 ES 权限副本过期也绝不越权泄露。
//   - ES 未配置 / 连接失败 / 超时 / 查询报错时自动降级为 MySQL LIKE 搜索；
//     ES 合法返回 0 条即如实返回 0 条（不因空结果降级，避免掩盖索引缺失）。
package search

import (
	"log"
	"sync/atomic"

	"doc-share/internal/config"
	"doc-share/internal/model"

	"gorm.io/gorm"
)

// Params 一次搜索的全部输入：权限范围 + 过滤条件 + 分页。
// 三个调用方按各自可见范围填写：
//   - Web DocsPage / AI search_documents：MemberPids 传用户实际参与的项目（owner ∪ 项目成员）
//   - OpenAPI list_docs：MemberPids 传 nil（密钥属主仅能搜自己的文档，admin 搜全部）
type Params struct {
	Query      string // 关键词，匹配 title/slug/content（html 整站的正文 manifest 不参与）
	UserID     uint
	IsAdmin    bool
	MemberPids []uint // 当前用户参与的项目 ID（非管理员的可见范围依据）
	Origin     string // "" 全部 | "mine" 我创建的 | "shared" 项目共享
	ProjectID  int    // -1 不过滤 | 0 未分组 | >0 指定项目
	CategoryID int    // -1 不过滤 | 0 未分类 | >0 指定分类
	Shared     int    // -1 不过滤 | 0 未分享 | 1 已分享
	Trash      bool   // true 仅软删文档（回收站视图）
	Offset     int
	Size       int
}

// DefaultParams 返回过滤项全部「不限」的默认参数（ProjectID/CategoryID/Shared = -1）。
func DefaultParams() Params {
	return Params{ProjectID: -1, CategoryID: -1, Shared: -1}
}

// orderCol 结果排序列：回收站按删除时间倒序，其余按更新时间倒序。
// ES 与 MySQL 两个后端都用同一排序，保证降级前后结果顺序一致。
func (p Params) orderCol() string {
	if p.Trash {
		return "deleted_at"
	}
	return "updated_at"
}

func (p Params) size() int {
	if p.Size <= 0 {
		return 15
	}
	return p.Size
}

// Service 文档搜索服务：内部持有 MySQL 与（可选的）ES 两个后端，对外暴露统一的
// Count / Page / ScopeQuery。所有搜索请求经此收敛，天然实现「三处同一套逻辑」。
type Service struct {
	db  *gorm.DB
	es  *ESClient // nil 表示未启用 ES，仅走 MySQL
	cfg config.SearchConfig

	ready atomic.Bool // ES 索引是否已全量就绪：首次全量同步成功前为 false，Count/Page 一律走 MySQL

	sync *Syncer // ES 数据同步器（未启用 ES 时为 no-op）
}

// New 构造搜索服务。cfg.ESURL 非空时初始化 ES 客户端（惰性建索引，不阻塞启动）。
func New(cfg config.SearchConfig, db *gorm.DB) *Service {
	s := &Service{db: db, cfg: cfg}
	if cfg.ESURL != "" {
		s.es = newESClient(cfg)
	}
	s.sync = &Syncer{svc: s}
	return s
}

// ESEnabled 报告是否配置了 ES 后端（供启动日志与诊断用）。
func (s *Service) ESEnabled() bool { return s.es != nil }

// setReady / isReady 控制「ES 是否已就绪可接管查询」：仅当首次全量同步成功后置位，
// 避免启动窗口期（索引半量）把文档列表绑定到 ES 新鲜度上。
func (s *Service) setReady(v bool) { s.ready.Store(v) }
func (s *Service) isReady() bool   { return s.ready.Load() }

// Count 返回命中总数：仅当 ES 已就绪时优先 ES，未配置/未就绪/报错时降级 MySQL。
func (s *Service) Count(p Params) (int64, error) {
	if s.es != nil && s.ready.Load() {
		if n, err := s.es.Count(p); err == nil {
			return n, nil
		} else {
			log.Printf("[search] ES 统计失败，降级 MySQL: %v", err)
		}
	}
	tx := applyQuery(s.ScopeQuery(p), p.Query)
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// Page 返回当前页命中的有序文档 ID：仅当 ES 已就绪时优先 ES，未配置/未就绪/报错时降级 MySQL。
// 调用方必须用 ScopeQuery().Where("id IN ?", ids) 回表做权限兜底后再渲染。
func (s *Service) Page(p Params) ([]uint, error) {
	if s.es != nil && s.ready.Load() {
		if ids, err := s.es.Search(p); err == nil {
			return ids, nil
		} else {
			log.Printf("[search] ES 搜索失败，降级 MySQL: %v", err)
		}
	}
	return s.mysqlPage(p)
}

func (s *Service) mysqlPage(p Params) ([]uint, error) {
	tx := applyQuery(s.ScopeQuery(p), p.Query)
	var ids []uint
	// id desc 作 tiebreaker，与 ES 排序一致，同时间戳下翻页稳定
	err := tx.Order(p.orderCol()+" desc, id desc").
		Offset(p.Offset).Limit(p.size()).Pluck("id", &ids).Error
	return ids, err
}

// ScopeQuery 构造「权限范围 + 过滤条件」的 GORM 查询（不含关键词、不含分页）。
// 它既是 MySQL 后端搜索的基础，也是所有调用方回表 hydration 的权限兜底：
// 即使 ES 侧权限副本过期，回表时这层过滤也会把不可见的文档剔除，杜绝越权。
func (s *Service) ScopeQuery(p Params) *gorm.DB {
	tx := s.db.Model(&model.Document{})
	if p.Trash {
		// 回收站视图：仅软删文档（与 DocsPage 一致，需 Unscoped 才能看到 deleted_at IS NOT NULL）
		tx = tx.Unscoped().Where("deleted_at IS NOT NULL")
	}
	if !p.IsAdmin {
		switch p.Origin {
		case "mine":
			tx = tx.Where("owner_id = ?", p.UserID)
		case "shared":
			if len(p.MemberPids) == 0 {
				tx = tx.Where("1 = 0") // 未参与任何项目，直接空集
			} else {
				tx = tx.Where("owner_id <> ? AND project_id IN ?", p.UserID, p.MemberPids)
			}
		default:
			if len(p.MemberPids) > 0 {
				tx = tx.Where("owner_id = ? OR project_id IN ?", p.UserID, p.MemberPids)
			} else {
				tx = tx.Where("owner_id = ?", p.UserID)
			}
		}
	} else if p.Origin == "mine" {
		tx = tx.Where("owner_id = ?", p.UserID)
	}
	if p.ProjectID >= 0 {
		tx = tx.Where("project_id = ?", p.ProjectID)
	}
	if p.CategoryID >= 0 {
		tx = tx.Where("category_id = ?", p.CategoryID)
	}
	if p.Shared == 0 || p.Shared == 1 {
		tx = tx.Where("is_shared = ?", p.Shared)
	}
	return tx
}

// applyQuery 在 MySQL 后端叠加关键词匹配：title/slug/content LIKE，
// html 整站文档的 content 只是 manifest（正文在对象存储），按类型显式排除。
func applyQuery(tx *gorm.DB, q string) *gorm.DB {
	if q == "" {
		return tx
	}
	like := "%" + q + "%"
	return tx.Where("title LIKE ? OR slug LIKE ? OR (type <> ? AND content LIKE ?)",
		like, like, model.DocTypeHTML, like)
}

// Reindex 把单篇文档同步进 ES（软删文档也会带上 deleted 标记，供回收站视图搜索）。
// 文档已不存在（硬删/彻底销毁）时从 ES 删除。best-effort：未启用 ES 时直接返回。
func (s *Service) Reindex(id uint) {
	if s.es == nil {
		return
	}
	s.sync.ReindexDoc(id)
}

// Deindex 从 ES 删除单篇文档（用于硬删除/彻底销毁）。best-effort。
func (s *Service) Deindex(id uint) {
	if s.es == nil {
		return
	}
	s.sync.DeindexDoc(id)
}

// StartSync 启动后台同步：先全量同步一次（bootstrap 存量数据），之后按 cfg.SyncMinutes
// 增量兜底（捕获写时双写漏网、软删/恢复等）。未启用 ES 时为 no-op。
func (s *Service) StartSync() {
	if s.es == nil {
		log.Printf("[search] 未配置 ES（search.es_url 为空），文档搜索使用 MySQL LIKE")
		return
	}
	s.sync.Start()
}
