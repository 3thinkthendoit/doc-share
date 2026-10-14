package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageMarshalPlainAndMultimodal(t *testing.T) {
	plain, err := json.Marshal(Message{Role: "user", Content: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), `"content":"hi"`) {
		t.Fatalf("plain: %s", plain)
	}

	m := Message{Role: "user", Parts: []ContentPart{
		{Type: "text", Text: "看图"},
		{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,xx"}},
	}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `"type":"image_url"`) || !strings.Contains(s, `"image_url"`) {
		t.Fatalf("multimodal: %s", s)
	}
	if strings.Contains(s, `"content":"看图"`) {
		t.Fatalf("should not use string content when Parts set: %s", s)
	}
}

func TestMessageMarshalOmitsEmptyContentWithTools(t *testing.T) {
	m := Message{Role: "assistant"}
	m.ToolCalls = []ToolCallMsg{{ID: "c1", Type: "function"}}
	m.ToolCalls[0].Function.Name = "f"
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"content"`) {
		t.Fatalf("empty content should be omitted: %s", data)
	}
	if !strings.Contains(string(data), `"tool_calls"`) {
		t.Fatalf("tool_calls required: %s", data)
	}
}
