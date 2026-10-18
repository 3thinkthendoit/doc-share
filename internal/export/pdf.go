package export

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"

	pdf "github.com/stephenafamo/goldmark-pdf"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

func toPDF(ctx context.Context, work, md, title string) ([]byte, error) {
	ff, err := resolveFonts()
	if err != nil {
		return nil, err
	}
	regular, err := os.ReadFile(ff.Regular)
	if err != nil {
		return nil, fmt.Errorf("读取 Regular 字体失败: %w", err)
	}
	bold, err := os.ReadFile(ff.Bold)
	if err != nil {
		return nil, fmt.Errorf("读取 Bold 字体失败: %w", err)
	}

	fpdf := pdf.NewFpdf(ctx, pdf.FpdfConfig{
		Title:       title,
		Orientation: "P",
		PaperSize:   "A4",
	}, nil)
	// 简体中文通常无独立 Italic；复用 Regular/Bold 满足四样式接口
	for _, pair := range []struct {
		style string
		data  []byte
		name  string
	}{
		{pdf.FontStyleRegular, regular, "Regular"},
		{pdf.FontStyleItalic, regular, "Italic"},
		{pdf.FontStyleBold, bold, "Bold"},
		{pdf.FontStyleBoldItalic, bold, "BoldItalic"},
	} {
		if err := fpdf.AddFont(fontFamilyNotoSC, pair.style, pair.data); err != nil {
			return nil, fmt.Errorf("加载 %s 字体失败: %w", pair.name, err)
		}
		if err := fpdf.Fpdf.Error(); err != nil {
			return nil, fmt.Errorf("加载 %s 字体失败: %w", pair.name, err)
		}
	}

	face := pdf.Font{
		CanUseForText: true,
		CanUseForCode: true,
		Family:        fontFamilyNotoSC,
		Type:          pdf.FontTypeCustom,
	}

	mdPDF := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRenderer(
			pdf.New(
				pdf.WithContext(ctx),
				pdf.WithPDF(fpdf),
				pdf.WithImageFS(http.FS(os.DirFS(work))),
				pdf.WithHeadingFont(face),
				pdf.WithBodyFont(face),
				pdf.WithCodeFont(face),
				pdf.WithEscapeHTML(false),
			),
		),
	)

	var buf bytes.Buffer
	if err := mdPDF.Convert([]byte(md), &buf); err != nil {
		return nil, fmt.Errorf("PDF 导出失败: %w", err)
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("PDF 导出结果为空")
	}
	return buf.Bytes(), nil
}
