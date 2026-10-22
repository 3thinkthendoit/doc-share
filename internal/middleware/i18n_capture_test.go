package middleware

import "testing"

func TestShouldCaptureSkipsStreamAndExport(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/console/api/chat/sessions/1/completions", false},
		{"/console/api/docs/9/export", false},
		{"/s/tok/export", false},
		{"/ps/tok/d/1/export", false},
		{"/console/docs", true},
		{"/console/api/docs/1", true},
		{"/s/abc", true},
		{"/openapi/v1/docs", false},
		{"/static/app.js", false},
	}
	for _, tc := range cases {
		if got := shouldCapture(tc.path); got != tc.want {
			t.Fatalf("shouldCapture(%q)=%v, want %v", tc.path, got, tc.want)
		}
	}
}
