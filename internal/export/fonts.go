package export

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/phpdave11/gofpdf"
)

const (
	fontFamilyNotoSC = "NotoSansSC"
	envFontDir       = "DOC_SHARE_FONT_DIR"
	fontCacheOKTTL  = 5 * time.Minute
	fontCacheErrTTL = 30 * time.Second
)

// fontFiles 运行时 PDF 所需的 Regular/Bold TTF 路径（斜体复用同文件）。
type fontFiles struct {
	Regular string
	Bold    string
}

// 测试可替换搜索目录。
var fontSearchDirsFn = fontSearchDirs

var (
	fontCacheMu   sync.Mutex
	fontCache     *fontFiles
	fontCacheErr  error
	fontCacheAt   time.Time
	fontCacheOK   bool
)

// invalidateFontCache 测试或字体文件变更后可调用。
func invalidateFontCache() {
	fontCacheMu.Lock()
	defer fontCacheMu.Unlock()
	fontCache = nil
	fontCacheErr = nil
	fontCacheOK = false
	fontCacheAt = time.Time{}
}

// resolveFonts 按环境变量与常见安装路径查找并可加载的 Noto Sans SC TTF。
func resolveFonts() (fontFiles, error) {
	fontCacheMu.Lock()
	if fontCacheOK {
		ttl := fontCacheOKTTL
		if fontCache == nil {
			ttl = fontCacheErrTTL
		}
		if time.Since(fontCacheAt) < ttl {
			if fontCache != nil {
				ff := *fontCache
				fontCacheMu.Unlock()
				return ff, nil
			}
			err := fontCacheErr
			fontCacheMu.Unlock()
			return fontFiles{}, err
		}
	}
	fontCacheMu.Unlock()

	ff, err := resolveFontsFrom(fontSearchDirsFn())

	fontCacheMu.Lock()
	fontCacheAt = time.Now()
	fontCacheOK = true
	if err != nil {
		fontCache = nil
		fontCacheErr = err
	} else {
		cp := ff
		fontCache = &cp
		fontCacheErr = nil
	}
	fontCacheMu.Unlock()
	return ff, err
}

func resolveFontsFrom(dirs []string) (fontFiles, error) {
	var lastErr error
	for _, dir := range dirs {
		ff, err := fontsInDir(dir)
		if err != nil {
			lastErr = err
			continue
		}
		if err := probeFonts(ff); err != nil {
			lastErr = err
			continue
		}
		return ff, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未配置字体目录")
	}
	return fontFiles{}, fmt.Errorf("未找到可用的 PDF 中文字体（Noto Sans SC TTF）：%w；请设置 %s", lastErr, envFontDir)
}

func fontSearchDirs() []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	add(os.Getenv(envFontDir))
	if wd, err := os.Getwd(); err == nil {
		add(filepath.Join(wd, "fonts", "noto-sc"))
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Join(filepath.Dir(exe), "fonts", "noto-sc"))
	}
	add("/usr/share/fonts/noto-sc")
	add("/usr/share/fonts/truetype/noto")
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, "fonts", "noto-sc"))
		add(filepath.Join(home, ".fonts", "noto-sc"))
	}
	return out
}

func fontsInDir(dir string) (fontFiles, error) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return fontFiles{}, fmt.Errorf("%s 不可用", dir)
	}
	regular := firstExisting(dir,
		"NotoSansSC-Regular.ttf",
		"chinese-simplified-400-normal.ttf",
	)
	bold := firstExisting(dir,
		"NotoSansSC-Bold.ttf",
		"chinese-simplified-700-normal.ttf",
	)
	if regular == "" {
		return fontFiles{}, fmt.Errorf("%s 缺少 Regular TTF", dir)
	}
	if bold == "" {
		bold = regular
	}
	return fontFiles{Regular: regular, Bold: bold}, nil
}

func firstExisting(dir string, names ...string) string {
	for _, name := range names {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Size() > 0 {
			return p
		}
	}
	return ""
}

func probeFonts(ff fontFiles) error {
	regular, err := os.ReadFile(ff.Regular)
	if err != nil {
		return fmt.Errorf("读取 Regular 失败: %w", err)
	}
	bold, err := os.ReadFile(ff.Bold)
	if err != nil {
		return fmt.Errorf("读取 Bold 失败: %w", err)
	}
	pdf := gofpdf.New("P", "pt", "A4", "")
	pdf.AddUTF8FontFromBytes(fontFamilyNotoSC, "", regular)
	if err := pdf.Error(); err != nil {
		return fmt.Errorf("Regular TTF 无法加载: %w", err)
	}
	pdf.AddUTF8FontFromBytes(fontFamilyNotoSC, "B", bold)
	if err := pdf.Error(); err != nil {
		return fmt.Errorf("Bold TTF 无法加载: %w", err)
	}
	pdf.AddPage()
	pdf.SetFont(fontFamilyNotoSC, "", 12)
	pdf.Cell(40, 10, "测")
	if err := pdf.Error(); err != nil {
		return fmt.Errorf("字体探测渲染失败: %w", err)
	}
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return fmt.Errorf("字体探测输出失败: %w", err)
	}
	return nil
}
