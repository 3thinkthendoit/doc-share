package handler

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuildDocInjectBodyShort(t *testing.T) {
	body := buildDocInjectBody("周报", 3, "第一段内容。", "改标题", 40000)
	if !strings.Contains(body, "content_version=3") {
		t.Fatalf("missing version: %s", body)
	}
	if !strings.Contains(body, "第一段内容。") {
		t.Fatalf("missing full body: %s", body)
	}
	if strings.Contains(body, "相关片段") {
		t.Fatalf("short doc should not segment: %s", body)
	}
}

func TestBuildDocInjectBodySegments(t *testing.T) {
	var b strings.Builder
	b.WriteString(strings.Repeat("填充填充填充填充。", 800)) // ~6400 runes of filler
	b.WriteString("这里是唯一关键词 MilestoneAlpha 所在段落，用于命中测试。")
	b.WriteString(strings.Repeat("尾巴尾巴尾巴尾巴。", 800))
	content := b.String()
	if utf8.RuneCountInString(content) <= aiInjectSegmentThreshold {
		t.Fatalf("fixture too short: %d", utf8.RuneCountInString(content))
	}
	body := buildDocInjectBody("长文", 1, content, "请修改 MilestoneAlpha 相关说明", 8000)
	if !strings.Contains(body, "相关片段") {
		t.Fatalf("expected segment inject, got: %.200s", body)
	}
	if !strings.Contains(body, "MilestoneAlpha") {
		t.Fatalf("segment miss keyword: %.200s", body)
	}
}

func TestVerifyReplaceResult(t *testing.T) {
	if got := verifyReplaceResult("hi world", "hello", "hi"); !strings.Contains(got, "校验通过") {
		t.Fatalf("got %q", got)
	}
	if got := verifyReplaceResult("hello world", "hello", "xyz"); !strings.Contains(got, "校验失败") {
		t.Fatalf("got %q", got)
	}
}

func TestInjectKeywords(t *testing.T) {
	keys := injectKeywords("请把《执行互助平台》里的团队日改成人天")
	if len(keys) == 0 {
		t.Fatal("expected keywords")
	}
	joined := strings.Join(keys, ",")
	if !strings.Contains(joined, "执行互助平台") && !strings.Contains(joined, "团队日") {
		t.Fatalf("unexpected keys: %v", keys)
	}
}
