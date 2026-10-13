package search

import (
	"log"
	"sync"
	"time"

	"doc-share/internal/model"
)

// Syncer 负责让 ES 索引与 MySQL 收敛一致。三层保障（从强到弱）：
//  1. 写时双写：文档变更后调 ReindexDoc/DeindexDoc，秒级新鲜；
//  2. 定时增量：每 cfg.SyncMinutes 按 updated_at/deleted_at 水位补漏（双写失败、软删/恢复）；
//  3. 启动全量：进程启动先全量重建一次，bootstrap 存量数据与修复历史漂移。
//
// 即便三层都漏了，调用方回表时的 ScopeQuery 权限兜底也保证不会越权泄露 —— 同步
// 只影响「能否被搜到」，不影响「是否有权看到」。
type Syncer struct {
	svc *Service

	mu        sync.Mutex
	watermark time.Time // 上次增量同步的时间水位
}

const (
	syncBatch = 100 // 全量/增量同步的单批文档数：过大会让单批 NDJSON 体积/耗时飙升，取小值更稳
	// syncColumns 仅选取 docToES 真正需要的列，避免把无关大字段读进内存（content 为全文检索必需，保留）
	syncColumns = "id,title,version,slug,type,content,content_version,owner_id,project_id,category_id,is_shared,created_at,updated_at,deleted_at"
)

// Start 启动后台同步协程：全量一次 + 定时增量。仅在启用 ES 时被调用。
// 全量成功前 ES 不接管查询（Service.ready 门闩），普通文档列表照常走 MySQL，
// 避免「索引尚未建好就对外提供半量/空结果」。
func (s *Syncer) Start() {
	interval := time.Duration(s.svc.cfg.SyncMinutes) * time.Minute
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[search] ES 同步协程 panic（已恢复，ES 保持降级 MySQL）: %v", r)
			}
		}()
		// 启动全量前删除旧索引重建：清除历史孤儿（硬删/销户残留），并让 mapping 跟随当前 IK 探测刷新。
		if err := s.svc.es.DeleteIndex(); err != nil {
			log.Printf("[search] ES 删除旧索引失败（继续尝试全量重建）: %v", err)
		}
		if err := s.svc.es.ensureIndex(); err != nil {
			log.Printf("[search] ES 建索引失败，稍后重试: %v", err)
		}
		// 水位取「全量开始时刻」：全量期间的并发写入 updated_at 必 >= start，
		// 叠加增量的重叠窗口即可全部补回，不会因全量耗时长而漏数据。
		start := time.Now()
		if n, err := s.FullSync(); err != nil {
			log.Printf("[search] ES 启动全量同步失败（ES 暂不接管查询，走 MySQL，下个周期重试）: %v", err)
		} else {
			log.Printf("[search] ES 启动全量同步完成，共 %d 篇文档；ES 检索就绪", n)
			s.svc.setReady(true)
		}
		s.setWatermark(start)
		for {
			time.Sleep(interval)
			if !s.svc.isReady() {
				// 首次全量未成功：持续重试全量，直到成功才让 ES 接管（避免半量索引对外服务）
				start := time.Now()
				if n, err := s.FullSync(); err != nil {
					log.Printf("[search] ES 全量同步重试失败: %v", err)
					continue
				} else {
					log.Printf("[search] ES 全量同步重试完成，共 %d 篇文档；ES 检索就绪", n)
					s.svc.setReady(true)
					s.setWatermark(start)
				}
				continue
			}
			if n, err := s.IncrementalSync(); err != nil {
				log.Printf("[search] ES 增量同步失败: %v", err)
				continue
			} else if n > 0 {
				log.Printf("[search] ES 增量同步 %d 篇文档", n)
			}
			s.setWatermark(time.Now())
		}
	}()
}

func (s *Syncer) setWatermark(t time.Time) {
	s.mu.Lock()
	s.watermark = t
	s.mu.Unlock()
}

func (s *Syncer) getWatermark() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watermark
}

// FullSync 全量重建：按 id 升序分批扫描所有文档（含软删，供回收站视图搜索）写入 ES。
func (s *Syncer) FullSync() (int, error) {
	total := 0
	var lastID uint
	for {
		var docs []model.Document
		if err := s.svc.db.Unscoped().Select(syncColumns).
			Where("id > ?", lastID).Order("id asc").Limit(syncBatch).
			Find(&docs).Error; err != nil {
			return total, err
		}
		if len(docs) == 0 {
			break
		}
		if err := s.svc.es.BulkIndex(docs); err != nil {
			return total, err
		}
		total += len(docs)
		lastID = docs[len(docs)-1].ID
		if len(docs) < syncBatch {
			break
		}
	}
	return total, nil
}

// IncrementalSync 增量同步：把水位之后有更新或（软）删除的文档重新写入 ES。
// 软删只改 deleted_at、恢复只清 deleted_at，两者都未必触碰 updated_at，故用 OR 一并覆盖；
// 水位回退一小段重叠窗口，避免边界毫秒级遗漏。
func (s *Syncer) IncrementalSync() (int, error) {
	wm := s.getWatermark()
	if wm.IsZero() {
		return s.FullSync()
	}
	since := wm.Add(-2 * time.Minute) // 重叠窗口，容忍时钟/提交时序抖动
	total := 0
	var lastID uint
	for {
		var docs []model.Document
		if err := s.svc.db.Unscoped().Select(syncColumns).
			Where("id > ? AND (updated_at >= ? OR deleted_at >= ?)", lastID, since, since).
			Order("id asc").Limit(syncBatch).
			Find(&docs).Error; err != nil {
			return total, err
		}
		if len(docs) == 0 {
			break
		}
		if err := s.svc.es.BulkIndex(docs); err != nil {
			return total, err
		}
		total += len(docs)
		lastID = docs[len(docs)-1].ID
		if len(docs) < syncBatch {
			break
		}
	}
	return total, nil
}

// ReindexDoc 同步单篇文档到 ES（写时双写入口）。文档已不存在（硬删/销毁）时反向删除。
// best-effort：所有错误仅记日志，绝不冒泡阻断主流程。
func (s *Syncer) ReindexDoc(id uint) {
	if s.svc.es == nil {
		return
	}
	var doc model.Document
	err := s.svc.db.Unscoped().First(&doc, id).Error
	if err != nil {
		// 文档已彻底不存在：从 ES 移除，避免留下孤儿（孤儿也会被回表兜底剔除，此处只是清干净）
		if derr := s.svc.es.DeleteDoc(id); derr != nil {
			log.Printf("[search] ES 删除孤儿文档 %d 失败: %v", id, derr)
		}
		return
	}
	if err := s.svc.es.IndexDoc(&doc); err != nil {
		log.Printf("[search] ES 索引文档 %d 失败: %v", id, err)
	}
}

// DeindexDoc 从 ES 删除单篇文档（硬删除/彻底销毁入口）。best-effort。
func (s *Syncer) DeindexDoc(id uint) {
	if s.svc.es == nil {
		return
	}
	if err := s.svc.es.DeleteDoc(id); err != nil {
		log.Printf("[search] ES 删除文档 %d 失败: %v", id, err)
	}
}
