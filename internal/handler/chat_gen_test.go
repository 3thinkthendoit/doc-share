package handler

import "testing"

func TestUnbindChatGenDoesNotPanicAndKeepsNewer(t *testing.T) {
	a := &App{}
	oldCancelled := false
	old := &chatGenToken{cancel: func() { oldCancelled = true }}
	a.bindChatGen(1, old)

	newerCancelled := false
	newer := &chatGenToken{cancel: func() { newerCancelled = true }}
	a.bindChatGen(1, newer)
	if !oldCancelled {
		t.Fatal("同会话新生成应取消上一次")
	}
	if newerCancelled {
		t.Fatal("新生成不应被自己的登记取消")
	}

	// 旧登记收尾不得删掉后来者，也不得因函数不可比较而 panic
	a.unbindChatGen(1, old)
	if !a.cancelChatGen(1) || !newerCancelled {
		t.Fatal("后来者的登记应仍在，且停止应调用它的取消函数")
	}
	if a.cancelChatGen(1) {
		t.Fatal("取消后登记应已清空")
	}
}

func TestUnbindChatGenRemovesCurrent(t *testing.T) {
	a := &App{}
	cancelled := false
	token := &chatGenToken{cancel: func() { cancelled = true }}
	a.bindChatGen(1, token)

	a.unbindChatGen(1, token)
	if cancelled {
		t.Fatal("摘掉登记不应调用取消函数")
	}
	if a.cancelChatGen(1) {
		t.Fatal("当前登记摘掉后，停止不应再命中")
	}
}
