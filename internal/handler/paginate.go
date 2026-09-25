package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// 前端可选每页条数
var webPageSizes = []int{15, 30, 50}

const (
	defaultPageSize = 15
	openMaxPageSize = 100
)

// Page 分页参数与结果
type Page struct {
	Page       int   // 当前页，从 1 起
	Size       int   // 每页条数
	Total      int64 // 总条数
	TotalPages int   // 总页数
	Offset     int   // SQL OFFSET
}

// parseWebPage 后台页面分页：size 仅允许 15/30/50，默认 15
func parseWebPage(c *gin.Context) Page {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", strconv.Itoa(defaultPageSize)))
	if page < 1 {
		page = 1
	}
	ok := false
	for _, s := range webPageSizes {
		if size == s {
			ok = true
			break
		}
	}
	if !ok {
		size = defaultPageSize
	}
	return Page{Page: page, Size: size, Offset: (page - 1) * size}
}

// parseOpenPage OpenAPI 分页：size 1~100，默认 15
func parseOpenPage(c *gin.Context) Page {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", strconv.Itoa(defaultPageSize)))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > openMaxPageSize {
		size = defaultPageSize
	}
	return Page{Page: page, Size: size, Offset: (page - 1) * size}
}

// withTotal 写入总数并回算总页数；若当前页越界则钳到最后一页并重算 Offset
func (p Page) withTotal(total int64) Page {
	p.Total = total
	if p.Size <= 0 {
		p.Size = defaultPageSize
	}
	p.TotalPages = int((total + int64(p.Size) - 1) / int64(p.Size))
	if p.TotalPages < 1 {
		p.TotalPages = 1 // 空列表也算 1 页，便于模板统一
	}
	if p.Page > p.TotalPages {
		p.Page = p.TotalPages
	}
	if p.Page < 1 {
		p.Page = 1
	}
	p.Offset = (p.Page - 1) * p.Size
	return p
}

// pageNums 生成数字页码序列（最多 7 个：窗口滑动，两端不足时贴边）
func pageNums(cur, total int) []int {
	if total < 1 {
		return nil
	}
	if total <= 7 {
		out := make([]int, total)
		for i := 0; i < total; i++ {
			out[i] = i + 1
		}
		return out
	}
	start := cur - 3
	if start < 1 {
		start = 1
	}
	end := start + 6
	if end > total {
		end = total
		start = end - 6
		if start < 1 {
			start = 1
		}
	}
	out := make([]int, 0, end-start+1)
	for i := start; i <= end; i++ {
		out = append(out, i)
	}
	return out
}

// pagerFields 注入模板共用分页字段
func pagerFields(pg Page, path, extra string) gin.H {
	return gin.H{
		"page":       pg.Page,
		"size":       pg.Size,
		"total":      pg.Total,
		"totalPages": pg.TotalPages,
		"pageSizes":  webPageSizes,
		"pagerPath":  path,
		"pagerExtra": extra, // 形如 &q=xx&owner=yy（勿含 page；size 已单独拼在链接里）
	}
}
