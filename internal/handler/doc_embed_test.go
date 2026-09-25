package handler

import "testing"

func TestContentHasFence(t *testing.T) {
	cases := []struct {
		content, lang string
		want          bool
	}{
		{"", "mindmap", false},
		{"hello", "mindmap", false},
		{"```mindmap\n{}\n```", "mindmap", true},
		{"```mindmap\n{}\n```", "excalidraw", false},
		{"```excalidraw\n{}\n```", "excalidraw", true},
		{"text\n```mindmap\n{}\n```\n", "mindmap", true},
		{"` ```mindmap ` not a fence", "mindmap", false},
		{"x```mindmap\n", "mindmap", false}, // 非行首
		{"```mindmap meta\n{}\n```", "mindmap", true},
	}
	for _, tc := range cases {
		if got := contentHasFence(tc.content, tc.lang); got != tc.want {
			t.Fatalf("contentHasFence(%q, %q)=%v want %v", tc.content, tc.lang, got, tc.want)
		}
	}
}

func TestDetectEmbedTagsEmpty(t *testing.T) {
	if got := detectEmbedTags(nil, nil); len(got) != 0 {
		t.Fatalf("nil db/docs want empty map, got %v", got)
	}
	if got := detectEmbedTags(nil, nil); got == nil {
		t.Fatal("want non-nil empty map")
	}
}
