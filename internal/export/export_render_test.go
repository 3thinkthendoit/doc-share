package export

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAvailable_docxAlwaysOK(t *testing.T) {
	if err := Available(FormatDOCX); err != nil {
		t.Fatal(err)
	}
}

func TestExport_DOCX_smoke(t *testing.T) {
	upload := t.TempDir()
	imgPath := filepath.Join(upload, "2026", "a.png")
	if err := os.MkdirAll(filepath.Dir(imgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTinyPNG(imgPath); err != nil {
		t.Fatal(err)
	}

	md := "正文段落。\n\n- 列表一项\n- 列表二项\n\n![图](/uploads/2026/a.png)\n"
	data, ctype, err := Export(context.Background(), FormatDOCX, Options{
		Title:     "测试文档",
		Markdown:  md,
		BaseURL:   "https://ex.com",
		UploadDir: upload,
		Timeout:   30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctype == "" || len(data) < 100 {
		t.Fatalf("unexpected docx: ctype=%q len=%d", ctype, len(data))
	}
	// OOXML zip magic
	if !bytes.HasPrefix(data, []byte("PK")) {
		t.Fatalf("docx should be zip, got prefix %q", data[:min(8, len(data))])
	}
}

func TestExport_PDF_smoke(t *testing.T) {
	if err := Available(FormatPDF); err != nil {
		t.Skipf("skip PDF smoke: %v", err)
	}

	upload := t.TempDir()
	imgPath := filepath.Join(upload, "2026", "a.png")
	if err := os.MkdirAll(filepath.Dir(imgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTinyPNG(imgPath); err != nil {
		t.Fatal(err)
	}

	md := "## 小节\n\n中文内容与 **粗体**。\n\n| 列 | 值 |\n| --- | --- |\n| a | 1 |\n\n![图](/uploads/2026/a.png)\n"
	data, ctype, err := Export(context.Background(), FormatPDF, Options{
		Title:     "PDF测试",
		Markdown:  md,
		BaseURL:   "https://ex.com",
		UploadDir: upload,
		Timeout:   60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ctype != "application/pdf" || len(data) < 100 {
		t.Fatalf("unexpected pdf: ctype=%q len=%d", ctype, len(data))
	}
	if !bytes.HasPrefix(data, []byte("%PDF")) {
		t.Fatalf("missing PDF header: %q", data[:min(8, len(data))])
	}
}

func writeTinyPNG(path string) error {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
