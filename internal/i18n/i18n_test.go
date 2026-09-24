package i18n

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

func loadDir(t *testing.T) *Bundle {
	t.Helper()
	b, err := Load(os.DirFS("../../web/locales"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

// TestLocaleParity 保证各语言词典键一致、无空值，且非中文语言不残留中文文案
func TestLocaleParity(t *testing.T) {
	b := loadDir(t)
	base := b.dicts[Default]
	cjk := regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]`)
	for _, l := range Supported {
		dict := b.dicts[l.Code]
		if dict == nil {
			t.Fatalf("locale %s 未加载", l.Code)
		}
		if len(dict) != len(base) {
			t.Errorf("%s 有 %d 个键，基准 %s 有 %d 个", l.Code, len(dict), Default, len(base))
		}
		var missing []string
		for k, v := range base {
			if dict[k] == "" {
				missing = append(missing, k+"="+v)
			}
		}
		for k := range dict {
			if _, ok := base[k]; !ok {
				missing = append(missing, "多余键 "+k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			t.Errorf("%s 词典与基准不一致：%v", l.Code, missing)
		}
		if l.Code == "en-US" || l.Code == "fr-FR" {
			for k, v := range dict {
				if cjk.MatchString(v) {
					t.Errorf("%s.%s 残留 CJK 文案：%s", l.Code, k, v)
				}
			}
		}
	}
}

// TestTZ 覆盖按 key、按中文原文、占位符与未收录文案四种查询方式
func TestTZ(t *testing.T) {
	b := loadDir(t)
	key := "common.save"
	src := b.dicts[Default][key]
	if got := b.T("en-US", key); got == src || got == "" {
		t.Fatalf("按 key 查询失败：%q", got)
	}
	if got := b.TZ("en-US", src); got != b.T("en-US", key) {
		t.Fatalf("按原文查询应等价于按 key：got %q want %q", got, b.T("en-US", key))
	}
	// 默认语言原样返回，且仍替换占位符
	if got := b.TZ(Default, "图片不能超过 {0} MB", 5); got != "图片不能超过 5 MB" {
		t.Errorf("默认语言占位符：%q", got)
	}
	if got := b.T("en-US", "err.imgTooBig", 5); got == "图片不能超过 {0} MB" || got == "" {
		t.Errorf("按 key 的占位符未生效：%q", got)
	}
	// 未收录文案降级为原文
	if got := b.TZ("ja-JP", "未收录的提示"); got != "未收录的提示" {
		t.Errorf("未收录文案应原样返回：%q", got)
	}
}

// TestTZKeyOnDefault 回归：模板里写 {{$.T "nav.dashboard"}}，默认语言下也必须返回中文，
// 早期实现直接把 key 原样输出，导航栏会渲染成 “nav.dashboard”
func TestTZKeyOnDefault(t *testing.T) {
	b := loadDir(t)
	zh := b.T(Default, "nav.dashboard")
	if zh == "nav.dashboard" || zh == "" {
		t.Fatalf("默认语言下 key 未解析：%q", zh)
	}
	if got := b.TZ(Default, "nav.dashboard"); got != zh {
		t.Errorf("TZ 传 key（默认语言）：%q want %q", got, zh)
	}
	// 中文原文与 key 两条路径在任意语言下等价
	if got := b.TZ("en-US", "nav.dashboard"); got != b.TZ("en-US", zh) {
		t.Errorf("TZ 传 key 与传原文不等价：%q vs %q", got, b.TZ("en-US", zh))
	}
	if got := b.TZ("", "nav.dashboard"); got != zh {
		t.Errorf("空语言应回退默认：%q", got)
	}
	// 带参文案在默认语言下也要完成占位符替换
	if got := b.TZ(Default, "docs.pageInfo", 1, 2, 3); got == "" || got == "docs.pageInfo" {
		t.Errorf("默认语言带参文案：%q", got)
	}
}

// TestResolve 校验语言协商优先级
func TestResolve(t *testing.T) {
	if got := Resolve("fr-FR", "ja,en;q=0.9"); got != "fr-FR" {
		t.Errorf("cookie 应优先于 Accept-Language：%q", got)
	}
	if got := Resolve("", "zh-TW,zh;q=0.9,en;q=0.8"); got != "zh-TW" {
		t.Errorf("Accept-Language：%q", got)
	}
	if got := Resolve("", "zh_Hant_HK"); got != "zh-TW" {
		t.Errorf("区域变体归一：%q", got)
	}
	if got := Resolve("bogus", ""); got != Default {
		t.Errorf("非法值应回落到默认语言：%q", got)
	}
	if got := Resolve("xx", "de,fr;q=0.9"); got != "fr-FR" {
		t.Errorf("cookie 非法时应继续看 Accept-Language：%q", got)
	}
}

// TestSourceJSON 校验前端注入映射：zh-CN 为空对象，其他语言含原文键
func TestSourceJSON(t *testing.T) {
	b := loadDir(t)
	if got := b.SourceJSON(Default); got != "{}" {
		t.Errorf("默认语言无需映射：%q", got)
	}
	got := b.SourceJSON("en-US")
	if got == "{}" || got == "" {
		t.Fatalf("en-US 映射为空")
	}
	// encoding/json 会将 < > & 转义，避免注入的译文提前闭合 <script>
	if i := indexOfTag(got); i >= 0 {
		t.Fatalf("映射中存在未转义的尖括号：%s", got[max(0, i-20):min(len(got), i+20)])
	}
}

func indexOfTag(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '<' || s[i] == '>' {
			return i
		}
	}
	return -1
}
