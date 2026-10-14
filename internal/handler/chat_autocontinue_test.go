package handler

import (
	"strings"
	"testing"
)

func TestShouldAutoContinue(t *testing.T) {
	unread := "（未读完，还有 12000 字符，请用 offset=8000 继续读取）"
	longOK := strings.Repeat("这篇文档前半部分主要讲了排期与里程碑。", 6) // >80 runes
	cases := []struct {
		name      string
		tools     string
		assistant string
		want      bool
	}{
		{"ask start", "", "六段读取已就绪，请回复「开始」我再执行", true},
		{"ask input continue", "", "请输入「继续」以便我接着改", true},
		{"clarify next doc not continue", "", "已改完标题，是否继续处理下一篇？", false},
		{"unread + short ack", unread, "好的", true},
		{"unread + empty", unread, "", true},
		{"unread + long final answer", unread, longOK, false},
		{"normal done", "[工具查询结果]\n已更新", "已把标题改好了。", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		if got := shouldAutoContinue(c.tools, c.assistant); got != c.want {
			t.Errorf("%s: shouldAutoContinue = %v, want %v", c.name, got, c.want)
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
