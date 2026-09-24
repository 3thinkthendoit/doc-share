// Package i18n 提供多语言词典加载、语言协商与翻译查询。
// 词典为嵌入的 JSON 文件（web/locales/<locale>.json），键为稳定的 i18n key。
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
)

// Lang 语言元信息，Name 使用各语言自称（无需翻译）
type Lang struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Supported 支持的语言列表（顺序即切换器展示顺序）
var Supported = []Lang{
	{"zh-CN", "简体中文"},
	{"zh-TW", "繁體中文"},
	{"ja-JP", "日本語"},
	{"en-US", "English"},
	{"fr-FR", "Français"},
}

// Default 缺省语言，也是其他语言缺失键的回退源
const Default = "zh-CN"

// CookieName 用户语言偏好 cookie
const CookieName = "ds_lang"

// Bundle 已加载的多语言词典
type Bundle struct {
	dicts   map[string]map[string]string
	src2key map[string]string // 中文原文（默认语言文案）→ i18n key，供 API 消息反向翻译
}

// Load 从 web/locales 目录（fs.FS 根即该目录）加载 *.json 词典
func Load(fsys fs.FS) (*Bundle, error) {
	b := &Bundle{dicts: make(map[string]map[string]string)}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read locales: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read locale %s: %w", e.Name(), err)
		}
		var dict map[string]string
		if err := json.Unmarshal(raw, &dict); err != nil {
			return nil, fmt.Errorf("parse locale %s: %w", e.Name(), err)
		}
		locale := strings.TrimSuffix(e.Name(), ".json")
		if !isSupported(locale) {
			continue
		}
		b.dicts[locale] = dict
	}
	if _, ok := b.dicts[Default]; !ok {
		return nil, fmt.Errorf("missing default locale %s", Default)
	}
	b.src2key = make(map[string]string, len(b.dicts[Default]))
	for k, v := range b.dicts[Default] {
		if _, exists := b.src2key[v]; !exists {
			b.src2key[v] = k // 同一条中文原文可能对应多个 key，取其一即可（译文相同）
		}
	}
	return b, nil
}

func isSupported(code string) bool {
	for _, l := range Supported {
		if l.Code == code {
			return true
		}
	}
	return false
}

// Valid 对外暴露的语言代码校验（供语言切换路由使用）
func Valid(code string) bool { return isSupported(code) }

// HasSource 判断中文原文是否已登记在默认词典里（即能从原文反查到 i18n 键）。
// 供测试使用：TZ 对未收录文案会降级返回原文，光看返回值无法区分「译完正好与原文同形」和「根本没收录」。
func (b *Bundle) HasSource(source string) bool {
	_, ok := b.src2key[strings.TrimSpace(source)]
	return ok
}

// T 查询翻译；支持 {0} {1} 占位符；缺失键回退 zh-CN，仍缺失则原样返回键
func (b *Bundle) T(locale, key string, args ...any) string {
	tpl, ok := b.dicts[locale][key]
	if !ok || tpl == "" {
		tpl = b.dicts[Default][key]
	}
	if tpl == "" {
		return key
	}
	if len(args) == 0 {
		return tpl
	}
	return format(tpl, args...)
}

// TZ 查询目标语言译文，入参可以是 i18n key，也可以是中文原文（默认语言文案）；
// 未收录的文案原样返回（降级而非报错）
func (b *Bundle) TZ(locale, source string, args ...any) string {
	if locale == "" {
		locale = Default
	}
	// 先按 key 查：模板里大量写的是 {{$.T "nav.docs"}} 这种稳定键
	if tpl, ok := b.dicts[locale][source]; ok && tpl != "" {
		return format(tpl, args...)
	}
	// 不是键（或目标语言缺该键）时回退默认词典，仍缺则原文即默认语言文案
	if locale == Default || b.src2key[strings.TrimSpace(source)] == "" {
		if tpl, ok := b.dicts[Default][source]; ok && tpl != "" {
			return format(tpl, args...)
		}
		return format(source, args...)
	}
	tr := b.T(locale, b.src2key[strings.TrimSpace(source)])
	if tr == "" {
		return format(source, args...)
	}
	return format(tr, args...)
}

func format(tpl string, args ...any) string {
	for i, a := range args {
		tpl = strings.ReplaceAll(tpl, fmt.Sprintf("{%d}", i), fmt.Sprint(a))
	}
	return tpl
}

// Format 对外露出占位符替换，供无词典时（降级）渲染带参文案
func Format(tpl string, args ...any) string { return format(tpl, args...) }

// TranslateMap 就地翻译 map 中所有可匹配的字符串值，返回改动条数。
// 用于把后台 API 返回的中文提示转换为当前语言，新增文案无需改 handler。
func (b *Bundle) TranslateMap(locale string, m map[string]any) int {
	n := 0
	for k, v := range m {
		if s, ok := v.(string); ok {
			if t := b.TZ(locale, s); t != s {
				m[k] = t
				n++
			}
		}
	}
	return n
}

// SourceJSON 生成前端 UI.t 用的翻译映射，包含两类键：
//   - 中文原文 → 译文（脚本里已有的中文字面量可直接查表，迁移风险低）
//   - i18n key → 译文（脚本里直接传 key 也能翻译，如 UI.t('proj.roleView')）
//
// 默认语言（zh-CN）也返回 key → 原文 的映射，保证 key 写法在任何语言下都有值。
func (b *Bundle) SourceJSON(locale string) string {
	if locale == "" {
		locale = Default
	}
	m := make(map[string]string, len(b.dicts[Default])*2)
	for k, v := range b.dicts[Default] {
		tr := b.T(locale, k)
		if v != k {
			m[v] = tr // 中文原文 → 译文
		}
		if tr != k {
			m[k] = tr // i18n key → 译文
		}
	}
	// 同一原文/key 冲突时后写覆盖，值一致或差异极小，可接受
	// encoding/json 默认转义 < > &，可安全嵌入 <script> 标签
	j, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(j)
}

// Resolve 依优先级确定语言：cookie 偏好 > Accept-Language > 默认
func Resolve(cookie, accept string) string {
	if l := normalize(cookie); l != "" {
		return l
	}
	for _, part := range strings.Split(accept, ",") {
		tag := strings.TrimSpace(strings.Split(part, ";")[0])
		if l := normalize(tag); l != "" {
			return l
		}
	}
	return Default
}

// normalize 把 BCP47 标签归一到支持的语言代码，不匹配返回空
func normalize(tag string) string {
	lt := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(tag, "_", "-")))
	if lt == "" {
		return ""
	}
	for _, l := range Supported {
		if strings.ToLower(l.Code) == lt {
			return l.Code
		}
	}
	switch {
	case strings.HasPrefix(lt, "zh-tw"), strings.HasPrefix(lt, "zh-hk"),
		strings.HasPrefix(lt, "zh-mo"), strings.HasPrefix(lt, "zh-hant"):
		return "zh-TW"
	case lt == "zh", strings.HasPrefix(lt, "zh-cn"), strings.HasPrefix(lt, "zh-hans"):
		return "zh-CN"
	case strings.HasPrefix(lt, "ja"):
		return "ja-JP"
	case strings.HasPrefix(lt, "fr"):
		return "fr-FR"
	case strings.HasPrefix(lt, "en"):
		return "en-US"
	}
	return ""
}
