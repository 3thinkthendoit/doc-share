// Package convert 将 PDF / doc / docx 文档转为 Markdown 文本。
// 全部为内存内转换，不落盘：docx 用标准库解压解析 OOXML，
// doc 解析 OLE 复合文档（FIB + 分片表），PDF 用 ledongthuc/pdf 提取文本。
package convert

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
)

var oleMagic = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// ToMarkdown 按魔数优先、扩展名兜底识别类型并转换为 Markdown
func ToMarkdown(name string, data []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case bytes.HasPrefix(data, oleMagic):
		return docToMarkdown(data)
	case bytes.HasPrefix(data, []byte("%PDF-")):
		return pdfToMarkdown(data)
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		return docxToMarkdown(data)
	case ext == ".doc":
		return docToMarkdown(data)
	case ext == ".pdf":
		return pdfToMarkdown(data)
	case ext == ".docx":
		return docxToMarkdown(data)
	}
	return "", fmt.Errorf("不支持的文件类型，仅支持 pdf / doc / docx")
}

// u16 / u32 小端读取（越界返回 0，调用方已做长度裁剪）
func u16(b []byte, off int) uint16 {
	if off+2 > len(b) {
		return 0
	}
	return uint16(b[off]) | uint16(b[off+1])<<8
}

func u32(b []byte, off int) uint32 {
	if off+4 > len(b) {
		return 0
	}
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

// decodeByLid 按文档语言 ID 解码 8 位文本：中文用 GBK，其余按 cp1252
func decodeByLid(b []byte, lid uint16) string {
	switch lid {
	case 0x0804, 0x0C04, 0x0404, 0x1004: // 简/繁中文
		s, err := simplifiedchinese.GBK.NewDecoder().Bytes(b)
		if err == nil {
			return string(s)
		}
	}
	s, err := charmap.Windows1252.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(s)
}

func decodeUTF16LE(b []byte) string {
	dec := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
	s, err := dec.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(s)
}

// looksLikeUTF16LE 粗判：偶数长度且大量字节为 0（ASCII 文本高位字节为 0）
func looksLikeUTF16LE(b []byte) bool {
	if len(b) < 4 || len(b)%2 != 0 {
		return false
	}
	zeros := 0
	n := 0
	for i := 1; i < len(b) && n < 256; i += 2 {
		n++
		if b[i] == 0 {
			zeros++
		}
	}
	return n > 0 && zeros*2 > n // 过半高位为 0 视为 UTF-16LE
}

// collapseBlank 把 3 个以上连续换行压缩为空行分隔（\n\n）
func collapseBlank(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	blank := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, strings.TrimRight(l, " \t"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
