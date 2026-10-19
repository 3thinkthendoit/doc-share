package handler

import (
	"strings"
	"testing"
)

func evCreate(ids ...int64) mutationEvidence {
	ev := mutationEvidence{createOK: true, writeOK: true}
	ev.docIDs = map[int64]bool{}
	for _, id := range ids {
		ev.docIDs[id] = true
	}
	return ev
}

func evWrite(ids ...int64) mutationEvidence {
	ev := mutationEvidence{writeOK: true}
	ev.docIDs = map[int64]bool{}
	for _, id := range ids {
		ev.docIDs[id] = true
	}
	return ev
}

func TestShouldAutoContinue(t *testing.T) {
	unread := "（未读完，还有 12000 字符，请用 offset=8000 继续读取）"
	longOK := strings.Repeat("这篇文档前半部分主要讲了排期与里程碑。", 6) // >80 runes
	cases := []struct {
		name      string
		tools     string
		assistant string
		ev        mutationEvidence
		want      bool
	}{
		{"ask start", "", "六段读取已就绪，请回复「开始」我再执行", mutationEvidence{}, true},
		{"ask input continue", "", "请输入「继续」以便我接着改", mutationEvidence{}, true},
		{"clarify next doc not continue", "", "已改完标题，是否继续处理下一篇？", mutationEvidence{}, false},
		{"unread + short ack", unread, "好的", mutationEvidence{}, true},
		{"unread + empty", unread, "", mutationEvidence{}, true},
		{"unread + long final answer", unread, longOK, mutationEvidence{}, false},
		{"normal done", "[工具查询结果]\n已更新", "已把标题改好了。", evWrite(3), false},
		{"empty", "", "", mutationEvidence{}, false},
		{"fake create without tool", "", "数独文档已生成，id=35，请查收。", mutationEvidence{}, true},
		{"create claim with matching id", "", "文档已创建：id=35", evCreate(35), false},
		{"create claim id not in evidence", "", "文档已创建：id=35", evCreate(7), true},
		{"replace does not wash create claim", "", "文档已创建 id=999", evWrite(3), true},
		{"list ids only not claim", "", "找到三篇：周报 id=3、纪要 id=8、草案 id=12。", mutationEvidence{}, false},
	}
	for _, c := range cases {
		if got := shouldAutoContinue(c.tools, c.assistant, c.ev); got != c.want {
			t.Errorf("%s: shouldAutoContinue = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestClaimsUnverifiedMutation(t *testing.T) {
	if !claimsUnverifiedMutation("已成功创建文档并保存。", mutationEvidence{}) {
		t.Fatal("应识别中文侧效宣称")
	}
	if !claimsUnverifiedMutation("I successfully created the document.", mutationEvidence{}) {
		t.Fatal("应识别英文侧效宣称")
	}
	if claimsUnverifiedMutation("已成功创建文档。", evCreate()) {
		t.Fatal("有匹配创建证据且未编造 id 时不应判谎")
	}
	if claimsUnverifiedMutation("当前列表：id=1 与 id=2", mutationEvidence{}) {
		t.Fatal("仅列举 id 不应判为侧效宣称")
	}
	if claimsUnverifiedMutation("已保存文档 id=7", evWrite(7)) {
		t.Fatal("保存宣称与证据 id 一致时不应判谎")
	}
	if !claimsUnverifiedMutation("已保存文档 id=7", evWrite(8)) {
		t.Fatal("保存宣称的 id 不在证据中应判谎")
	}
	negatives := []string{
		"已创建好提纲，尚未写入文档",
		"文档已生成，请确认是否保存到库",
		"I have not created the document yet",
		"The document was not successfully created",
		"已复制到剪贴板",
	}
	for _, s := range negatives {
		if claimsUnverifiedMutation(s, mutationEvidence{}) {
			t.Fatalf("不应判为侧效宣称: %s", s)
		}
	}
}

func TestInjectBudgetPerDoc(t *testing.T) {
	if got := injectBudgetPerDoc(1); got != aiPrereadMaxChars {
		t.Fatalf("single = %d, want %d", got, aiPrereadMaxChars)
	}
	if got := injectBudgetPerDoc(2); got != aiPrereadMaxChars/2 {
		t.Fatalf("two = %d, want %d", got, aiPrereadMaxChars/2)
	}
	if got := injectBudgetPerDoc(10); got != aiToolMaxResult {
		t.Fatalf("many = %d, want floor %d", got, aiToolMaxResult)
	}
}

func TestToolStepDocID(t *testing.T) {
	if got := toolStepDocID("create_document", `{"title":"x"}`, "文档已创建：{{doc:42}}（id=42，type=markdown）"); got != 42 {
		t.Fatalf("create from result = %d, want 42", got)
	}
	if got := toolStepDocID("update_document", `{"id":7,"content":"a"}`, "ok"); got != 7 {
		t.Fatalf("update from args = %d, want 7", got)
	}
}
