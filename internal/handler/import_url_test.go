package handler

import "testing"

func TestBlockImportContentWrite(t *testing.T) {
	pending := importPendingMarkdown("https://example.com/a")
	if !blockImportContentWrite(pending, "用户改过的正文") {
		t.Fatal("占位期间应拒绝正文写入")
	}
	if !blockImportContentWrite("# 已导入\n", pending) {
		t.Fatal("占位正文不能盖掉已导入内容")
	}
	if blockImportContentWrite("# 已导入\n", "# 用户后来的修改\n") {
		t.Fatal("导入完成后应允许正常保存")
	}
}
