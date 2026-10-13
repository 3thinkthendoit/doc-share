package search

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"doc-share/internal/config"
	"doc-share/internal/model"
)

// ESClient 极简 Elasticsearch REST 客户端：仅用标准库 net/http 直连 ES 的 JSON API，
// 不引入官方 SDK（零新依赖，与项目手摇风格一致，且不受客户端/服务端版本锁定）。
//
// 所有方法在「未配置 / 连接失败 / 超时 / 非 2xx / 解析失败」时返回 error，
// 由 Service 层据此降级 MySQL —— ES 只是加速器，永远不是正确性/权限的真相源。
type ESClient struct {
	base   string // ES 基础 URL，如 http://es:8080（无尾部斜杠）
	index  string
	user   string       // HTTP Basic 用户名（与 apiKey 二选一）
	pass   string       // HTTP Basic 密码
	apiKey string       // ES API Key（Authorization: ApiKey <key>）；非空时优先于 Basic
	hc     *http.Client // 查询/单文档写入：超时 = es_timeout_ms（短，超时即降级 MySQL）
	bulkHC *http.Client // 批量同步专用：超时远大于查询（大批量 NDJSON 写入慢，不能被 2s 查询超时误杀）

	indexReady atomic.Bool // 是否已确认索引存在（避免每次搜索都 HEAD）；索引被外部删除时复位
	idxMu      sync.Mutex  // 串行化建索引，避免并发重复 PUT 触发 resource_already_exists
	ikMu       sync.Mutex  // 保护 ikEnabled
	ikEnabled  *bool       // 缓存 analysis-ik 插件探测结果（nil = 尚未探测/上次探测失败）
}

func newESClient(cfg config.SearchConfig) *ESClient {
	queryTimeout := time.Duration(cfg.ESTimeoutMS) * time.Millisecond
	bulkTimeout := 60 * time.Second // 批量同步预算：远大于查询超时，避免全量/增量因短超时静默失败
	if queryTimeout > bulkTimeout {
		bulkTimeout = queryTimeout
	}
	return &ESClient{
		base:   strings.TrimRight(cfg.ESURL, "/"),
		index:  cfg.ESIndex,
		user:   cfg.ESUsername,
		pass:   cfg.ESPassword,
		apiKey: cfg.ESApiKey,
		hc:     &http.Client{Timeout: queryTimeout},
		bulkHC: &http.Client{Timeout: bulkTimeout},
	}
}

// setAuth 统一注入认证头：配置了 API Key 则用 ApiKey 方案（ES 推荐的密钥认证），
// 否则回退 HTTP Basic（用户名/密码）；两者都未配置则不带认证（内网关闭安全认证场景）。
func (c *ESClient) setAuth(req *http.Request) {
	switch {
	case c.apiKey != "":
		req.Header.Set("Authorization", "ApiKey "+c.apiKey)
	case c.user != "":
		req.SetBasicAuth(c.user, c.pass)
	}
}

// esDoc 索引里的文档结构：文本字段供全文检索，其余为权限/过滤字段的冗余副本。
// 权限副本可能与 MySQL 短暂不一致，故调用方回表时必须再套 ScopeQuery 兜底。
type esDoc struct {
	ID         uint   `json:"id"`
	Title      string `json:"title"`
	Slug       string `json:"slug"`
	Type       string `json:"type"`
	Content    string `json:"content"`
	Version    string `json:"version"`
	ContentVer int64  `json:"content_version"`
	OwnerID    uint   `json:"owner_id"`
	ProjectID  uint   `json:"project_id"`
	CategoryID uint   `json:"category_id"`
	IsShared   bool   `json:"is_shared"`
	Deleted    bool   `json:"deleted"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	DeletedAt  string `json:"deleted_at,omitempty"`
}

// docToES 把 GORM 文档模型映射为索引文档。html 整站的 content 只是 manifest
// （正文在对象存储），置空避免污染全文检索（与 MySQL 侧按类型排除 content 一致）。
func docToES(d *model.Document) esDoc {
	content := d.Content
	if d.Type == model.DocTypeHTML {
		content = ""
	}
	ed := esDoc{
		ID:         d.ID,
		Title:      d.Title,
		Slug:       d.Slug,
		Type:       d.Type,
		Content:    content,
		Version:    d.Version,
		ContentVer: d.ContentVersion,
		OwnerID:    d.OwnerID,
		ProjectID:  d.ProjectID,
		CategoryID: d.CategoryID,
		IsShared:   d.IsShared,
		Deleted:    d.DeletedAt.Valid,
		CreatedAt:  d.CreatedAt.Format(time.RFC3339),
		UpdatedAt:  d.UpdatedAt.Format(time.RFC3339),
	}
	if d.DeletedAt.Valid {
		ed.DeletedAt = d.DeletedAt.Time.Format(time.RFC3339)
	}
	return ed
}

// do 发起一次 ES REST 请求。path 以 "/" 开头拼在 base 之后。返回响应体（调用方解析）。
func (c *ESClient) do(method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		return nil, err
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.setAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 300 {
		// 索引被外部删除/数据卷丢失：复位 indexReady，下次调用自动重建，避免永久静默降级 MySQL
		if resp.StatusCode == http.StatusNotFound && bytes.Contains(data, []byte("index_not_found_exception")) {
			c.indexReady.Store(false)
		}
		return data, fmt.Errorf("ES %s %s 返回 %d: %.200s", method, path, resp.StatusCode, string(data))
	}
	return data, nil
}

// ensureIndex 确认索引存在，不存在则按显式 mapping 创建。首次成功后置 indexReady 短路，
// 避免每次搜索/写入都多一次 HEAD。并发下用 idxMu 串行化 + 双重检查，杜绝重复 PUT。
func (c *ESClient) ensureIndex() error {
	if c.indexReady.Load() {
		return nil
	}
	c.idxMu.Lock()
	defer c.idxMu.Unlock()
	if c.indexReady.Load() { // 等锁期间可能已被其他 goroutine 建好
		return nil
	}
	return c.ensureIndexLocked()
}

func (c *ESClient) ensureIndexLocked() error {
	req, err := http.NewRequest(http.MethodHead, c.base+"/"+c.index, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		c.indexReady.Store(true)
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("ES 检查索引返回 %d", resp.StatusCode)
	}
	// 建索引时探测是否装了 IK 中文分词插件：装了则 title/content 用 ik_max_word 建索引、
	// ik_smart 搜索（中文按词切分，召回/相关度更好）；未装则回退 ES 标准分析器
	// （中文按单字切分），保证「没装 IK 也不至于建索引失败、整个 ES 不可用」。
	ik := c.detectIK()
	mapping := buildMapping(ik)
	if _, err := c.do(http.MethodPut, "/"+c.index, mapping); err != nil {
		return err
	}
	if ik {
		log.Printf("[search] ES 索引 %s 使用 IK 中文分词（ik_max_word/ik_smart）", c.index)
	} else {
		log.Printf("[search] ES 未检测到 analysis-ik 插件，索引 %s 使用标准分析器（中文按单字切分）", c.index)
	}
	c.indexReady.Store(true)
	return nil
}

// DeleteIndex 删除整个索引（不存在视为成功）并复位 indexReady，供启动全量重建前清场：
// 既清除历史孤儿文档（硬删/销户残留），又让 mapping 跟随当前 IK 探测结果刷新。
// 注：仅适用于单实例部署（多实例共享同一索引时，一个实例重建会短暂影响其他）。
func (c *ESClient) DeleteIndex() error {
	req, err := http.NewRequest(http.MethodDelete, c.base+"/"+c.index, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	c.indexReady.Store(false)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("ES 删除索引返回 %d", resp.StatusCode)
	}
	return nil
}

// detectIK 探测集群是否安装 analysis-ik 插件。仅在成功拿到插件列表时缓存结果；
// 探测失败（超时/权限不足）时返回 false 但**不缓存**，以便索引重建时重试——
// 避免因一次瞬时探测失败就把 mapping 永久固化为标准分析器（中文分词静默退化）。
func (c *ESClient) detectIK() bool {
	c.ikMu.Lock()
	defer c.ikMu.Unlock()
	if c.ikEnabled != nil {
		return *c.ikEnabled
	}
	data, err := c.do(http.MethodGet, "/_cat/plugins?format=json", nil)
	if err != nil {
		log.Printf("[search] ES 探测 analysis-ik 插件失败，本次按未安装处理（不缓存，建索引重试时会再探测）: %v", err)
		return false
	}
	var plugins []struct {
		Component string `json:"component"`
	}
	ik := false
	if json.Unmarshal(data, &plugins) == nil {
		for _, p := range plugins {
			if p.Component == "analysis-ik" {
				ik = true
				break
			}
		}
	}
	c.ikEnabled = &ik
	return ik
}

// buildMapping 构造索引 mapping。ik=true 时 title/content 采用 IK 分词器
// （写入 ik_max_word 细粒度切分、搜索 ik_smart 粗粒度，兼顾召回与准确）；
// 否则用 ES 默认标准分析器。slug 始终为 keyword（走 wildcard 子串匹配）。
func buildMapping(ik bool) map[string]any {
	textField := func() map[string]any {
		if ik {
			return map[string]any{"type": "text", "analyzer": "ik_max_word", "search_analyzer": "ik_smart"}
		}
		return map[string]any{"type": "text"}
	}
	return map[string]any{
		"mappings": map[string]any{
			"properties": map[string]any{
				"id":              map[string]any{"type": "long"}, // 显式映射，供排序 tiebreaker 使用
				"title":           textField(),
				"slug":            map[string]any{"type": "keyword"},
				"type":            map[string]any{"type": "keyword"},
				"content":         textField(),
				"version":         map[string]any{"type": "keyword"},
				"content_version": map[string]any{"type": "long"},
				"owner_id":        map[string]any{"type": "long"},
				"project_id":      map[string]any{"type": "long"},
				"category_id":     map[string]any{"type": "long"},
				"is_shared":       map[string]any{"type": "boolean"},
				"deleted":         map[string]any{"type": "boolean"},
				"created_at":      map[string]any{"type": "date"},
				"updated_at":      map[string]any{"type": "date"},
				"deleted_at":      map[string]any{"type": "date"},
			},
		},
	}
}

// buildQuery 把 Params 翻译成 ES bool 查询：
//   - filter 段承载权限范围与 project/category/shared/trash 过滤（不参与打分）；
//   - must 段承载关键词全文匹配（title/content 分词匹配，slug 子串通配，html 正文排除）。
//
// 返回 (query, matchNone)：matchNone=true 表示权限范围必然为空（如 shared 但未参与任何项目），
// 调用方应直接返回 0 结果，不必请求 ES。
func (c *ESClient) buildQuery(p Params) (map[string]any, bool) {
	filters := []map[string]any{
		{"term": map[string]any{"deleted": p.Trash}},
	}
	if !p.IsAdmin {
		switch p.Origin {
		case "mine":
			filters = append(filters, map[string]any{"term": map[string]any{"owner_id": p.UserID}})
		case "shared":
			if len(p.MemberPids) == 0 {
				return nil, true // 未参与任何项目 → 空集
			}
			filters = append(filters, map[string]any{"terms": map[string]any{"project_id": p.MemberPids}})
			filters = append(filters, map[string]any{"bool": map[string]any{
				"must_not": []map[string]any{{"term": map[string]any{"owner_id": p.UserID}}},
			}})
		default:
			if len(p.MemberPids) > 0 {
				filters = append(filters, map[string]any{"bool": map[string]any{
					"should": []map[string]any{
						{"term": map[string]any{"owner_id": p.UserID}},
						{"terms": map[string]any{"project_id": p.MemberPids}},
					},
					"minimum_should_match": 1,
				}})
			} else {
				filters = append(filters, map[string]any{"term": map[string]any{"owner_id": p.UserID}})
			}
		}
	} else if p.Origin == "mine" {
		filters = append(filters, map[string]any{"term": map[string]any{"owner_id": p.UserID}})
	}
	if p.ProjectID >= 0 {
		filters = append(filters, map[string]any{"term": map[string]any{"project_id": p.ProjectID}})
	}
	if p.CategoryID >= 0 {
		filters = append(filters, map[string]any{"term": map[string]any{"category_id": p.CategoryID}})
	}
	if p.Shared == 0 || p.Shared == 1 {
		filters = append(filters, map[string]any{"term": map[string]any{"is_shared": p.Shared == 1}})
	}

	boolQ := map[string]any{"filter": filters}
	if q := strings.TrimSpace(p.Query); q != "" {
		// slug 走 wildcard 子串匹配，需转义用户输入里的通配符元字符，避免 "*" 命中全部或触发 ES 报错
		esc := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`).Replace(q)
		// title/content 用 operator:and：要求关键词分词后全部命中，对齐 MySQL 降级时
		// 「整串子串 LIKE」的严格口径，避免多词搜索在 ES/MySQL 两后端结果集大幅漂移。
		boolQ["must"] = []map[string]any{{"bool": map[string]any{
			"should": []map[string]any{
				{"match": map[string]any{"title": map[string]any{"query": q, "operator": "and"}}},
				{"wildcard": map[string]any{"slug": map[string]any{"value": "*" + esc + "*", "case_insensitive": true}}},
				{"bool": map[string]any{
					"must":     []map[string]any{{"match": map[string]any{"content": map[string]any{"query": q, "operator": "and"}}}},
					"must_not": []map[string]any{{"term": map[string]any{"type": model.DocTypeHTML}}},
				}},
			},
			"minimum_should_match": 1,
		}}}
	}
	return map[string]any{"bool": boolQ}, false
}

// 高亮占位标记：ES highlight 的 pre/post_tags 用控制字符（正常正文不会出现），
// 渲染时先 HTML 转义再替换为 <mark>，兼顾「标黄」与 XSS 安全（见 RenderHighlight）。
const (
	hlPre  = "\x00"
	hlPost = "\x01"
)

// searchBody 组装 _search 请求体：只取 _id（_source=false），按 orderCol 倒序，from/size 分页。
func (c *ESClient) searchBody(p Params, withHits, withHL bool) (map[string]any, bool) {
	query, matchNone := c.buildQuery(p)
	if matchNone {
		return nil, true
	}
	body := map[string]any{
		"query":            query,
		"track_total_hits": true,
		"_source":          false,
		"sort": []map[string]any{
			{p.orderCol(): map[string]any{"order": "desc"}},
			{"id": map[string]any{"order": "desc"}}, // tiebreaker：同时间戳下翻页稳定，避免重复/漏条
		},
	}
	if withHits {
		body["from"] = p.Offset
		body["size"] = p.size()
		// 仅 Web 搜索页需要高亮（AI/MCP 走 Search，不付这份开销）：
		// content 取 1 个 ~120 字符片段，占位标记渲染时替换为 <mark>。
		if withHL && strings.TrimSpace(p.Query) != "" {
			body["highlight"] = map[string]any{
				"pre_tags":  []string{hlPre},
				"post_tags": []string{hlPost},
				"fields": map[string]any{
					"content": map[string]any{"fragment_size": 120, "number_of_fragments": 1},
				},
			}
		}
	} else {
		body["size"] = 0 // 仅统计总数
	}
	return body, false
}

type esSearchResp struct {
	Hits struct {
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
		Hits []struct {
			ID        string              `json:"_id"`
			Highlight map[string][]string `json:"highlight"`
		} `json:"hits"`
	} `json:"hits"`
}

// Search 返回当前页命中的有序文档 ID（不请求高亮，供 AI/MCP 等不需要摘要的入口）。
func (c *ESClient) Search(p Params) ([]uint, error) {
	ids, _, err := c.search(p, false)
	return ids, err
}

// SearchWithHighlight 返回有序文档 ID + 每篇的正文高亮片段（原始片段含占位标记，
// 调用方用 RenderHighlight 转安全 HTML）。无正文片段（仅标题/slug 命中、HTML 文档）时该 ID 不在 map 中。
func (c *ESClient) SearchWithHighlight(p Params) ([]uint, map[uint]string, error) {
	return c.search(p, true)
}

func (c *ESClient) search(p Params, withHL bool) ([]uint, map[uint]string, error) {
	if err := c.ensureIndex(); err != nil {
		return nil, nil, err
	}
	body, matchNone := c.searchBody(p, true, withHL)
	if matchNone {
		return nil, nil, nil // 权限范围必然为空
	}
	data, err := c.do(http.MethodPost, "/"+c.index+"/_search", body)
	if err != nil {
		return nil, nil, err
	}
	var resp esSearchResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, nil, err
	}
	ids := make([]uint, 0, len(resp.Hits.Hits))
	snippets := make(map[uint]string, len(resp.Hits.Hits))
	for _, h := range resp.Hits.Hits {
		id, err := strconv.ParseUint(h.ID, 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, uint(id))
		if frags, ok := h.Highlight["content"]; ok && len(frags) > 0 {
			snippets[uint(id)] = frags[0]
		}
	}
	return ids, snippets, nil
}

// MarkMatches 在**原文**（未转义）上给 re 的每个命中包上内部占位标记，
// 调用方随后用 RenderHighlight 完成「转义 + 占位→<mark>」。
// 在原文上打标记可避免查询词切中 &amp; 等转义实体内部导致文本被改写。
func MarkMatches(raw string, re *regexp.Regexp) string {
	return re.ReplaceAllString(raw, hlPre+"$1"+hlPost)
}

// RenderHighlight 把 ES 高亮片段转成可安全内嵌的 HTML：先整体转义（正文为用户内容，防 XSS），
// 再把控制字符占位标记替换为 <mark>/</mark>。
func RenderHighlight(frag string) string {
	s := html.EscapeString(frag)
	s = strings.ReplaceAll(s, hlPre, "<mark>")
	s = strings.ReplaceAll(s, hlPost, "</mark>")
	return s
}

// Count 返回命中总数（size=0，只读 total）。
func (c *ESClient) Count(p Params) (int64, error) {
	if err := c.ensureIndex(); err != nil {
		return 0, err
	}
	body, matchNone := c.searchBody(p, false, false)
	if matchNone {
		return 0, nil
	}
	data, err := c.do(http.MethodPost, "/"+c.index+"/_search", body)
	if err != nil {
		return 0, err
	}
	var resp esSearchResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return 0, err
	}
	return resp.Hits.Total.Value, nil
}

// IndexDoc 写入/更新单篇文档（以 doc id 作 _id，天然幂等）。
func (c *ESClient) IndexDoc(d *model.Document) error {
	if err := c.ensureIndex(); err != nil {
		return err
	}
	ed := docToES(d)
	_, err := c.do(http.MethodPut, "/"+c.index+"/_doc/"+strconv.FormatUint(uint64(ed.ID), 10), ed)
	return err
}

// DeleteDoc 从索引删除单篇文档；文档本就不存在（404）视为成功（幂等）。
func (c *ESClient) DeleteDoc(id uint) error {
	if err := c.ensureIndex(); err != nil {
		return err
	}
	path := "/" + c.index + "/_doc/" + strconv.FormatUint(uint64(id), 10)
	req, err := http.NewRequest(http.MethodDelete, c.base+path, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("ES 删除文档 %d 返回 %d", id, resp.StatusCode)
	}
	return nil
}

// BulkIndex 批量写入（NDJSON _bulk API），用于全量/增量同步。空切片直接返回。
func (c *ESClient) BulkIndex(docs []model.Document) error {
	if len(docs) == 0 {
		return nil
	}
	if err := c.ensureIndex(); err != nil {
		return err
	}
	var buf bytes.Buffer
	for i := range docs {
		ed := docToES(&docs[i])
		meta, _ := json.Marshal(map[string]any{"index": map[string]any{
			"_index": c.index,
			"_id":    strconv.FormatUint(uint64(ed.ID), 10),
		}})
		src, _ := json.Marshal(ed)
		buf.Write(meta)
		buf.WriteByte('\n')
		buf.Write(src)
		buf.WriteByte('\n')
	}
	req, err := http.NewRequest(http.MethodPost, c.base+"/_bulk", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	c.setAuth(req)
	resp, err := c.bulkHC.Do(req) // 批量写入用长超时客户端，避免大批量 NDJSON 被查询超时（2s）误杀
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("ES _bulk 返回 %d: %.200s", resp.StatusCode, string(data))
	}
	var bulkResp struct {
		Errors bool `json:"errors"`
	}
	_ = json.Unmarshal(data, &bulkResp)
	if bulkResp.Errors {
		return fmt.Errorf("ES _bulk 部分文档写入失败: %.200s", string(data))
	}
	return nil
}
