package export

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fabiomarini/wordingo"
)

// wordingo 仅支持 data URI 图片；把已物化的 media/* 转成内嵌图。
var mediaImgRe = regexp.MustCompile(`!\[([^\]]*)\]\((media/[^)\s]+)(\s+"[^"]*"|\s+'[^']*')?\)`)

func toDOCX(ctx context.Context, work, md, title string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_ = title // 标题已由 EnsureTitleHeading 写入正文

	md = embedLocalMediaAsDataURI(work, md)
	doc, err := wordingo.CreateFromMarkdown(md)
	if err != nil {
		return nil, fmt.Errorf("DOCX 导出失败: %w", err)
	}
	defer doc.Close()

	var buf bytes.Buffer
	if _, err := doc.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("写入 DOCX 失败: %w", err)
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("DOCX 导出结果为空")
	}
	return buf.Bytes(), nil
}

func embedLocalMediaAsDataURI(work, md string) string {
	return mediaImgRe.ReplaceAllStringFunc(md, func(m string) string {
		sub := mediaImgRe.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		alt := sub[1]
		rel := sub[2] // media/001.png
		abs, ok := resolveUnderRoot(work, rel)
		if !ok {
			return m
		}
		data, err := os.ReadFile(abs)
		if err != nil || len(data) == 0 {
			return m
		}
		// gif/webp 等应在 materialize 阶段已转 PNG；此处再兜底一次
		out, ext, err := normalizeImageBytes(data, filepath.Ext(abs))
		if err != nil {
			return m
		}
		mime := mimeFromImageBytes(out, ext)
		if mime == "" {
			return m
		}
		b64 := base64.StdEncoding.EncodeToString(out)
		return fmt.Sprintf("![%s](data:%s;base64,%s)", alt, mime, b64)
	})
}

// 保留旧名供测试；逻辑已并入 mimeFromImageBytes。
func mimeFromExt(ext string, data []byte) string {
	return mimeFromImageBytes(data, strings.ToLower(ext))
}
