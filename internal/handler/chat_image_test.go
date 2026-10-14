package handler

import "testing"

func TestParseChatImageContent(t *testing.T) {
	url, text := parseChatImageContent("![chat-image](/uploads/202610/abc.png)\n\n看看这张图")
	if url != "/uploads/202610/abc.png" || text != "看看这张图" {
		t.Fatalf("got url=%q text=%q", url, text)
	}
	url, text = parseChatImageContent("![chat-image](/uploads/x.png)")
	if url != "/uploads/x.png" || text != "" {
		t.Fatalf("image-only: url=%q text=%q", url, text)
	}
	url, text = parseChatImageContent("纯文字没有图")
	if url != "" || text != "纯文字没有图" {
		t.Fatalf("text-only: url=%q text=%q", url, text)
	}
}

func TestFormatChatImageContent(t *testing.T) {
	got := formatChatImageContent("/uploads/a.png", "说明")
	want := "![chat-image](/uploads/a.png)\n\n说明"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if formatChatImageContent("/uploads/a.png", "") != "![chat-image](/uploads/a.png)" {
		t.Fatal("image-only format")
	}
}

func TestChatUserContentOK(t *testing.T) {
	if !chatUserContentOK("hello") {
		t.Fatal("text ok")
	}
	if !chatUserContentOK("![chat-image](/uploads/a.png)") {
		t.Fatal("image ok")
	}
	if chatUserContentOK("   ") {
		t.Fatal("empty should fail")
	}
}
