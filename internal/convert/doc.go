package convert

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/richardlehane/mscfb"
)

// docToMarkdown 解析 Word 97-2003 二进制文档（OLE 复合文件）：
// WordDocument 流中的 FIB 定位分片表（Clx），按 PCD 分片取出文本
//（UTF-16LE 或 8 位码页），最后清理控制字符输出为段落 Markdown。
func docToMarkdown(data []byte) (string, error) {
	rd, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("无法读取 Word 文档（OLE 复合文件）：%v", err)
	}
	var wordDoc, tbl []byte
	for {
		f, err := rd.Next()
		if err != nil {
			break
		}
		switch f.Name {
		case "WordDocument":
			wordDoc, _ = io.ReadAll(f)
		case "1Table", "0Table":
			if b, err := io.ReadAll(f); err == nil && len(b) > 0 {
				if f.Name == "1Table" || len(tbl) == 0 {
					tbl = b
				}
			}
		}
	}
	if len(wordDoc) == 0 {
		return "", fmt.Errorf("Word 文档缺少 WordDocument 流，文件可能已损坏")
	}

	text := extractDocText(wordDoc, tbl)
	out := docTextToMarkdown(text)
	if out == "" {
		return "", fmt.Errorf("doc 中没有可提取的文本内容")
	}
	return out, nil
}

// extractDocText 从 FIB + 分片表提取全文
func extractDocText(wd, tbl []byte) string {
	wIdent := u16(wd, 0)
	if wIdent != 0xA5EC {
		return "" // 不是 Word 二进制格式
	}
	flags := u16(wd, 0x0A)
	if flags&0x0100 != 0 {
		return "" // 加密文档
	}
	lid := u16(wd, 6)
	fcMin := int64(u32(wd, 0x18))
	fcMac := int64(u32(wd, 0x1C))
	fcClx := int64(u32(wd, 0x1A2))
	lcbClx := int64(u32(wd, 0x1A6))

	// 首选分片表（Word97 文档绝大多数带 Clx）
	if lcbClx > 0 && fcClx+int64(lcbClx) <= int64(len(tbl)) {
		if s := extractByPieces(wd, tbl[fcClx:fcClx+lcbClx], lid); s != "" {
			return s
		}
	}
	// 兜底：FIB 直接给出文本范围（老版本/无分片表文档）
	if fcMin >= 0 && fcMac > fcMin && fcMac <= int64(len(wd)) {
		raw := wd[fcMin:fcMac]
		if looksLikeUTF16LE(raw) {
			return decodeUTF16LE(raw)
		}
		return decodeByLid(raw, lid)
	}
	return ""
}

// extractByPieces 解析 CLX：跳过 Prc 节，读 Pcdt 的 PlcPcd，逐分片解码文本
func extractByPieces(wd, clx []byte, lid uint16) string {
	var sb strings.Builder
	pos := 0
	for pos < len(clx) {
		switch clx[pos] {
		case 1: // Prc（属性修订），跳过
			if pos+3 > len(clx) {
				return sb.String()
			}
			pos += 3 + int(u16(clx, pos+1))
		case 2: // Pcdt
			if pos+5 > len(clx) {
				return sb.String()
			}
			lcb := int(u32(clx, pos+1))
			if pos+5+lcb > len(clx) {
				lcb = len(clx) - pos - 5
			}
			if lcb < 4 {
				return sb.String()
			}
			plc := clx[pos+5 : pos+5+lcb]
			n := (len(plc) - 4) / 12
			for i := 0; i < n; i++ {
				cpStart := int(u32(plc, i*4))
				cpEnd := int(u32(plc, (i+1)*4))
				if cpEnd <= cpStart {
					continue
				}
				pcdOff := (n+1)*4 + i*8
				fc := u32(plc, pcdOff+2)
				count := cpEnd - cpStart
				if fc&0x40000000 != 0 { // 压缩分片：8 位编码，偏移已 ×2
					off := int(fc&0x3FFFFFFF) / 2
					if off+count > len(wd) {
						count = len(wd) - off
					}
					if count <= 0 {
						continue
					}
					sb.WriteString(decodeByLid(wd[off:off+count], lid))
				} else { // UTF-16LE 分片
					off := int(fc & 0x3FFFFFFF)
					if off+count*2 > len(wd) {
						count = (len(wd) - off) / 2
					}
					if count <= 0 {
						continue
					}
					sb.WriteString(decodeUTF16LE(wd[off : off+count*2]))
				}
			}
			return sb.String()
		default:
			return sb.String() // 未知结构，返回已提取部分
		}
	}
	return sb.String()
}

// docTextToMarkdown 清理 Word 控制字符并整理为段落
func docTextToMarkdown(s string) string {
	var b strings.Builder
	fldDepth := 0
	inInstr := false
	for _, r := range s {
		if inInstr { // 域指令（\x13 到 \x14 之间）丢弃，保留域结果
			switch r {
			case 0x14:
				inInstr = false
			case 0x15:
				fldDepth--
				if fldDepth <= 0 {
					fldDepth, inInstr = 0, false
				}
			}
			continue
		}
		switch r {
		case '\r':
			b.WriteString("\n\n")
		case '\v', '\f':
			b.WriteString("\n")
		case 0x07: // 表格单元格/行结尾
			b.WriteString(" ")
		case 0x13:
			fldDepth++
			inInstr = true
		case 0x14, 0x15:
			if fldDepth > 0 && r == 0x15 {
				fldDepth--
			}
		case 0x1E:
			b.WriteString("-")
		case 0x1F, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x08:
			// 软连字符、图片锚点等控制符丢弃
		case 0xA0:
			b.WriteString(" ")
		case '\n':
			b.WriteString("\n")
		default:
			if r >= 0x20 {
				b.WriteRune(r)
			}
		}
	}
	return collapseBlank(b.String())
}
