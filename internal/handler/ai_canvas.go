package handler

import (
	"encoding/json"
	"fmt"
	"strings"

	"doc-share/internal/model"
)

// validateAIDocContent 画布正文必须是对应格式，避免把 JSON/XML 覆盖成普通文字。
func validateAIDocContent(docType, content string) error {
	switch docType {
	case model.DocTypeMindmap, model.DocTypeBoard:
		if !json.Valid([]byte(strings.TrimSpace(content))) {
			return fmt.Errorf("%s 正文必须是合法 JSON", docType)
		}
	case model.DocTypeDrawio:
		if !strings.Contains(content, "<mxfile") {
			return fmt.Errorf("drawio 正文必须是 mxfile XML")
		}
	}
	return nil
}

// aiCanvasDocHint 画布 content 格式说明（与 MCP CANVAS_DOC / 前端编辑器对齐），
// 拼进 create_document / create_template 工具描述，供模型在 tools/list 阶段学习。
const aiCanvasDocHint = "" +
	"【画布 content】mindmap/board 必须是 JSON 字符串；drawio 是 mxfile XML 字符串。" +
	"【mindmap】最小结构：{\"data\":{\"text\":\"中心主题\"},\"children\":[{\"data\":{\"text\":\"分支\"}}]}。" +
	"【board】Excalidraw：{\"type\":\"excalidraw\",\"version\":2,\"source\":\"doc-share\",\"elements\":[...],\"appState\":{\"viewBackgroundColor\":\"#ffffff\"},\"files\":{}}。" +
	"【drawio】未压缩 mxfile XML，content 传 XML 本身不是 JSON。"
