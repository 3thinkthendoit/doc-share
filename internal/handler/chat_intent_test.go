package handler

import "testing"

func TestReadIntentID(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"仔细阅读 id=19 的文档", 19},
		{"阅读文档19", 19},
		{"看看 d=19 的文档", 19},
		{"查看文档 id:19 内容", 19},
		{"读一下 文档 19", 19},
		// 无关动词/无 ID：不得命中
		{"查看 3 个项目", 0},
		{"第 2 条记录", 0},
		// 单字母 d 必须带分隔符：英文句子不得误伤
		{"compare d 3 docs", 0},
		{"please read d 3", 0},
	}
	for _, c := range cases {
		if got := readIntentID(c.in); got != c.want {
			t.Errorf("readIntentID(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestExtractDocIDs(t *testing.T) {
	cases := []struct {
		in   string
		want []int64
	}{
		{"把文档19的团队日改为人天", []int64{19}},
		{"id=19 和 文档 22 对比一下", []int64{19, 22}},
		{"d=7", []int64{7}},
		// 无 ID 标记：空
		{"帮我把团队日改成人天", nil},
		// 单字母 d 无分隔符：不误伤英文
		{"compare d 3 docs", nil},
	}
	for _, c := range cases {
		got := extractDocIDs(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("extractDocIDs(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("extractDocIDs(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

func TestReadIntentTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 书名号 / 引号优先
		{"仔细阅读这篇《执行互助平台》的内容", "执行互助平台"},
		{"帮我看看「架构设计文档」", "架构设计文档"},
		// 无书名号：去动词与修饰词后作为检索词
		{"仔细阅读这篇 执行互助平台·分三期开发计划与排期（版本 v1.0.5）", "执行互助平台·分三期开发计划与排期"},
		{"阅读 互助平台原始需求 正文", "互助平台原始需求"},
		// 无动词：不命中
		{"《执行互助平台》写得怎么样", ""},
		// 去掉修饰后太短：不命中
		{"看看这个", ""},
	}
	for _, c := range cases {
		if got := readIntentTitle(c.in); got != c.want {
			t.Errorf("readIntentTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractResultIDs(t *testing.T) {
	res := "1. 《A》（id=3，Markdown v1.0.2）\n2. 《B》（id=7，Markdown v1.0.2）"
	if ids := extractResultIDs(res); len(ids) != 2 || ids[0] != 3 || ids[1] != 7 {
		t.Fatalf("got %v", ids)
	}
	if ids := extractResultIDs("《A》（id=3）\n重复 id=3"); len(ids) != 1 || ids[0] != 3 {
		t.Fatalf("去重失败: %v", ids)
	}
	if ids := extractResultIDs("无结果"); len(ids) != 0 {
		t.Fatalf("got %v", ids)
	}
}

func TestExtractDocIDsCapAndDedup(t *testing.T) {
	got := extractDocIDs("id=1 id=1 文档2 文档3 文档4")
	if len(got) != 3 {
		t.Fatalf("上限 3 且去重失败: %v", got)
	}
	if got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("顺序错误: %v", got)
	}
}
