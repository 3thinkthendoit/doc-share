package handler

import "testing"

func TestPageNums(t *testing.T) {
	got := pageNums(1, 5)
	if len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Fatalf("small range: %v", got)
	}
	got = pageNums(5, 20)
	if len(got) != 7 || got[0] != 2 || got[6] != 8 {
		t.Fatalf("window: %v", got)
	}
	got = pageNums(19, 20)
	if got[0] != 14 || got[6] != 20 {
		t.Fatalf("end window: %v", got)
	}
}

func TestWithTotalClamp(t *testing.T) {
	p := Page{Page: 99, Size: 15}.withTotal(20)
	if p.Page != 2 || p.TotalPages != 2 || p.Offset != 15 {
		t.Fatalf("%+v", p)
	}
	p = Page{Page: 1, Size: 15}.withTotal(0)
	if p.TotalPages != 1 || p.Offset != 0 {
		t.Fatalf("empty: %+v", p)
	}
}
