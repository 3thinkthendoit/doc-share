package handler

import (
	"context"
	"testing"
)

func TestUnbindChatGenDoesNotPanicAndKeepsNewer(t *testing.T) {
	a := &App{}
	oldCancelled := false
	old := &chatGenToken{cancel: func() { oldCancelled = true }}
	if !a.tryBindChatGen(1, 10, old) {
		t.Fatal("首次绑定应成功")
	}

	newerCancelled := false
	newer := &chatGenToken{cancel: func() { newerCancelled = true }}
	if !a.tryBindChatGen(1, 10, newer) {
		t.Fatal("同会话替换应成功")
	}
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
	if !a.tryBindChatGen(1, 10, token) {
		t.Fatal("绑定应成功")
	}

	a.unbindChatGen(1, token)
	if cancelled {
		t.Fatal("摘掉登记不应调用取消函数")
	}
	if a.cancelChatGen(1) {
		t.Fatal("当前登记摘掉后，停止不应再命中")
	}
}

func TestTryBindChatGenUserCap(t *testing.T) {
	a := &App{}
	var tokens []*chatGenToken
	for i := uint(1); i <= aiMaxConcurrentGensPerUser; i++ {
		tok := &chatGenToken{cancel: func() {}}
		tokens = append(tokens, tok)
		if !a.tryBindChatGen(i, 42, tok) {
			t.Fatalf("第 %d 路应成功", i)
		}
	}
	extra := &chatGenToken{cancel: func() {}}
	if a.tryBindChatGen(99, 42, extra) {
		t.Fatal("超限应拒绝")
	}
	// 其他用户不受影响
	other := &chatGenToken{cancel: func() {}}
	if !a.tryBindChatGen(100, 7, other) {
		t.Fatal("其他用户应可绑定")
	}
	// 同会话替换不占第二槽
	repl := &chatGenToken{cancel: func() {}}
	if !a.tryBindChatGen(1, 42, repl) {
		t.Fatal("同会话替换不应受上限拒绝")
	}
	// 释放一路后可再占
	a.unbindChatGen(1, repl)
	again := &chatGenToken{cancel: func() {}}
	if !a.tryBindChatGen(99, 42, again) {
		t.Fatal("释放后应可再绑定")
	}
}

// TestGenCtxIndependentOfClientCtx 回归：刷新取消的是 clientCtx，不得带动 genCtx。
// 后台生成必须用 context.Background 衍生的 genCtx，否则断 SSE 会停 LLM。
func TestGenCtxIndependentOfClientCtx(t *testing.T) {
	genCtx, genCancel := context.WithCancel(context.Background())
	defer genCancel()
	clientCtx, clientCancel := context.WithCancel(context.Background())
	clientCancel() // 模拟刷新/关页
	if clientCtx.Err() == nil {
		t.Fatal("clientCtx 应已取消")
	}
	if genCtx.Err() != nil {
		t.Fatal("genCtx 不得随客户端断连而取消")
	}
	select {
	case <-clientCtx.Done():
	default:
		t.Fatal("client Done 未触发")
	}
	select {
	case <-genCtx.Done():
		t.Fatal("gen Done 不应触发")
	default:
	}
}

func TestSessionGenerating(t *testing.T) {
	a := &App{}
	if a.sessionGenerating(1) {
		t.Fatal("空闲会话不应 generating")
	}
	tok := &chatGenToken{cancel: func() {}}
	a.tryBindChatGen(1, 1, tok)
	if !a.sessionGenerating(1) {
		t.Fatal("绑定后应 generating")
	}
	a.unbindChatGen(1, tok)
	if a.sessionGenerating(1) {
		t.Fatal("解绑后不应 generating")
	}
}
