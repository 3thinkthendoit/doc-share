package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"doc-share/internal/llm"
)

func TestToolStepLabel(t *testing.T) {
	got := toolStepLabel("replace_section", `{"id":28,"old_text":"a","new_text":"b"}`, "ok", true)
	if got != "Replaced section · doc 28" {
		t.Fatalf("got %q", got)
	}
}

func TestToolStepLabelFailReason(t *testing.T) {
	got := toolStepLabel("create_html_page", `{"title":"x"}`,
		"工具执行失败：html 须为完整页面（含 <!DOCTYPE html> 或 <html>）；CSS/JS 请写在页面内", false)
	if !strings.HasPrefix(got, "create_html_page failed · ") {
		t.Fatalf("want fail reason suffix, got %q", got)
	}
	if !strings.Contains(got, "须为完整页面") {
		t.Fatalf("want error detail, got %q", got)
	}
	if strings.Contains(got, "工具执行失败：") {
		t.Fatalf("prefix should be stripped: %q", got)
	}

	empty := toolStepLabel("create_html_page", `{}`, "", false)
	if empty != "create_html_page failed" {
		t.Fatalf("empty result: %q", empty)
	}

	var tc llm.ToolCallMsg
	tc.Function.Name = "create_html_page"
	tc.Function.Arguments = `{"title":"t"}`
	raw := toolStepEndJSON(tc, "工具执行失败：html 不能为空", false)
	if !strings.Contains(raw, `"ok":false`) {
		t.Fatalf("ok false: %s", raw)
	}
	if !strings.Contains(raw, "create_html_page failed") || !strings.Contains(raw, "html 不能为空") {
		t.Fatalf("label/summary: %s", raw)
	}
}

func TestToolStepEndJSONDiff(t *testing.T) {
	var tc llm.ToolCallMsg
	tc.Function.Name = "replace_section"
	tc.Function.Arguments = `{"id":28,"old_text":"旧段落","new_text":"新段落"}`
	raw := toolStepEndJSON(tc, "已完成局部替换", true)
	var wrap struct {
		ToolStep struct {
			Phase string `json:"phase"`
			OK    bool   `json:"ok"`
			Label string `json:"label"`
			Diff  *struct {
				ID  int64  `json:"id"`
				Old string `json:"old"`
				New string `json:"new"`
			} `json:"diff"`
		} `json:"tool_step"`
	}
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil {
		t.Fatal(err)
	}
	if wrap.ToolStep.Phase != "end" || !wrap.ToolStep.OK {
		t.Fatalf("bad phase/ok: %+v", wrap.ToolStep)
	}
	if wrap.ToolStep.Diff == nil || wrap.ToolStep.Diff.ID != 28 {
		t.Fatalf("missing diff: %s", raw)
	}
	if !strings.Contains(wrap.ToolStep.Diff.Old, "旧") || !strings.Contains(wrap.ToolStep.Diff.New, "新") {
		t.Fatalf("diff text: %+v", wrap.ToolStep.Diff)
	}
}
