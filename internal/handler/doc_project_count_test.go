package handler

import (
	"testing"

	"doc-share/internal/model"
	"doc-share/internal/search"
)

func TestProjectDocCounts(t *testing.T) {
	a, shared := newAuthApp(t)
	users, err := loadTestUsers(a)
	if err != nil {
		t.Fatalf("load users: %v", err)
	}
	pid := shared.ProjectID

	// 属主未分组 ×2；属主项目内再 ×1（加上种子共享文档共 2）；路人私有未分组不应计入属主
	seed := []model.Document{
		{Title: "未分组甲", Slug: "cntaaaaa", Content: "a", OwnerID: users["owner"].ID, ProjectID: 0},
		{Title: "未分组乙", Slug: "cntbbbbb", Content: "b", OwnerID: users["owner"].ID, ProjectID: 0},
		{Title: "项目丙", Slug: "cntccccc", Content: "c", OwnerID: users["owner"].ID, ProjectID: pid},
		{Title: "路人丁", Slug: "cntddddd", Content: "d", OwnerID: users["outsider"].ID, ProjectID: 0},
	}
	for i := range seed {
		if err := a.DB.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	ownerParams := search.DefaultParams()
	ownerParams.UserID = users["owner"].ID
	ownerParams.MemberPids = []uint{pid}

	t.Run("owner_visible_counts", func(t *testing.T) {
		m, total := a.projectDocCounts(ownerParams)
		if m[0] != 2 {
			t.Fatalf("未分组=%d want 2", m[0])
		}
		if m[pid] != 2 { // 种子共享文档 + 项目丙
			t.Fatalf("项目=%d want 2", m[pid])
		}
		if total != 4 {
			t.Fatalf("全部=%d want 4", total)
		}
	})

	t.Run("ignores_project_category_shared_filters", func(t *testing.T) {
		p := ownerParams
		p.ProjectID = int(pid)
		p.CategoryID = 0
		p.Shared = 1
		p.Query = "不会影响"
		m, total := a.projectDocCounts(p)
		if m[0] != 2 || m[pid] != 2 || total != 4 {
			t.Fatalf("侧栏计数不应受 project/category/shared/q 影响: m0=%d mpid=%d total=%d", m[0], m[pid], total)
		}
	})

	t.Run("member_sees_project_not_owner_ungrouped", func(t *testing.T) {
		p := search.DefaultParams()
		p.UserID = users["viewm"].ID
		p.MemberPids = []uint{pid}
		m, total := a.projectDocCounts(p)
		if m[0] != 0 {
			t.Fatalf("成员不应看到属主未分组文档: %d", m[0])
		}
		if m[pid] != 2 {
			t.Fatalf("成员应看到项目内文档: %d want 2", m[pid])
		}
		if total != 2 {
			t.Fatalf("成员全部=%d want 2", total)
		}
	})

	t.Run("outsider_private_not_leaked", func(t *testing.T) {
		p := search.DefaultParams()
		p.UserID = users["outsider"].ID
		m, total := a.projectDocCounts(p)
		if m[0] != 1 {
			t.Fatalf("路人自己的未分组=%d want 1", m[0])
		}
		if m[pid] != 0 {
			t.Fatalf("路人不应计入他人项目: %d", m[pid])
		}
		if total != 1 {
			t.Fatalf("路人全部=%d want 1", total)
		}
	})

	t.Run("origin_mine_excludes_shared_only_view", func(t *testing.T) {
		// viewm 无自有文档：origin=mine 应为 0
		p := search.DefaultParams()
		p.UserID = users["viewm"].ID
		p.MemberPids = []uint{pid}
		p.Origin = "mine"
		_, total := a.projectDocCounts(p)
		if total != 0 {
			t.Fatalf("origin=mine 成员全部=%d want 0", total)
		}
	})
}
