package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"doc-share/internal/model"

	"github.com/gin-gonic/gin"
)

// TestDeleteUserBlocksAdmin 禁止删除任意 role=admin；普通用户仍可删；页面不对 admin 渲染删除入口
func TestDeleteUserBlocksAdmin(t *testing.T) {
	a, _ := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	admin := users["boss"]
	viewer := users["viewm"]
	actor := users["owner"] // 非管理员操作者在单测里直接调 handler，鉴权由路由负责

	t.Run("refuse_admin", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: itoa(int(admin.ID))}}
		asUser(c, actor, http.MethodDelete, "/console/api/users/"+itoa(int(admin.ID)), "")
		a.DeleteUser(c)
		assertCode(t, w, http.StatusBadRequest)
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("json: %v", err)
		}
		if !strings.Contains(body["error"].(string), "管理员") {
			t.Fatalf("error = %v, want 管理员相关提示", body["error"])
		}
		var n int64
		a.DB.Model(&model.User{}).Where("id = ?", admin.ID).Count(&n)
		if n != 1 {
			t.Fatal("管理员不应被删除")
		}
	})

	t.Run("allow_viewer", func(t *testing.T) {
		w, c := newCtx()
		c.Params = gin.Params{{Key: "id", Value: itoa(int(viewer.ID))}}
		asUser(c, actor, http.MethodDelete, "/console/api/users/"+itoa(int(viewer.ID)), "")
		a.DeleteUser(c)
		assertCode(t, w, http.StatusOK)
		var n int64
		a.DB.Model(&model.User{}).Where("id = ?", viewer.ID).Count(&n)
		if n != 0 {
			t.Fatal("普通用户应已被删除")
		}
	})

	t.Run("page_hides_admin_delete", func(t *testing.T) {
		// 重新搭库：上一子测已删 viewm，页面断言只关心 admin 行无 del
		a2, _ := newAuthApp(t)
		users2, err := loadTestUsers(a2)
		if err != nil {
			t.Fatalf("load users: %v", err)
		}
		render := newRenderApp(t, a2.DB)
		w, c := newCtx()
		asUser(c, users2["boss"], http.MethodGet, "/console/users", "")
		render.UsersPage(c)
		assertCode(t, w, http.StatusOK)
		body := w.Body.String()
		// admin 行：data-role="admin" 的 tr 内不应出现 data-act="del"
		idx := strings.Index(body, `data-role="admin"`)
		if idx < 0 {
			t.Fatal("页面应含管理员行")
		}
		chunk := body[idx:]
		if end := strings.Index(chunk, "</tr>"); end > 0 {
			chunk = chunk[:end]
		}
		if strings.Contains(chunk, `data-act="del"`) {
			t.Fatal("管理员行不应渲染删除按钮")
		}
		// viewer 行仍应有删除
		vIdx := strings.Index(body, `data-role="viewer"`)
		if vIdx < 0 {
			t.Fatal("页面应含普通用户行")
		}
		vChunk := body[vIdx:]
		if end := strings.Index(vChunk, "</tr>"); end > 0 {
			vChunk = vChunk[:end]
		}
		if !strings.Contains(vChunk, `data-act="del"`) {
			t.Fatal("普通用户行应保留删除按钮")
		}
	})
}
