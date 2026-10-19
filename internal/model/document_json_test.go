package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocumentJSONOmitsCreatorIP(t *testing.T) {
	doc := Document{
		Title:         "t",
		CreatorIP:     "203.0.113.8",
		CreatorRegion: "中国 广东 深圳",
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, "203.0.113.8") || strings.Contains(s, "creator_ip") {
		t.Fatalf("creator IP leaked into JSON: %s", s)
	}
	if !strings.Contains(s, "中国 广东 深圳") {
		t.Fatalf("creator region should remain visible: %s", s)
	}
}
