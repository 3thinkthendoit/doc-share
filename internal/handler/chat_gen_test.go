package handler

import (
	"context"
	"testing"
	"time"
)

func TestUnbindChatGenDoesNotCancelOnSecondBind(t *testing.T) {
	a := &App{}
	oldCancelled := false
	old := &chatGenToken{cancel: func() { oldCancelled = true }}
	if ok, why := a.tryBindChatGen(1, 10, old); !ok || why != "" {
		t.Fatalf("首次绑定应成功 ok=%v why=%s", ok, why)
	}

	newer := &chatGenToken{cancel: func() {}}
	if ok, why := a.tryBindChatGen(1, 10, newer); ok || why != "session" {
		t.Fatalf("同会话应拒绝且不替换 ok=%v why=%s", ok, why)
	}
	if oldCancelled {
		t.Fatal("同会话新请求不得取消正在跑的生成")
	}
	if !a.cancelChatGen(1) || !oldCancelled {
		t.Fatal("停止按钮仍应取消当前生成")
	}
}

func TestUnbindChatGenRemovesCurrent(t *testing.T) {
	a := &App{}
	cancelled := false
	token := &chatGenToken{cancel: func() { cancelled = true }}
	if ok, _ := a.tryBindChatGen(1, 10, token); !ok {
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
		if ok, why := a.tryBindChatGen(i, 42, tok); !ok || why != "" {
			t.Fatalf("第 %d 路应成功 why=%s", i, why)
		}
	}
	extra := &chatGenToken{cancel: func() {}}
	if ok, why := a.tryBindChatGen(99, 42, extra); ok || why != "cap" {
		t.Fatalf("超限应拒绝 ok=%v why=%s", ok, why)
	}
	other := &chatGenToken{cancel: func() {}}
	if ok, _ := a.tryBindChatGen(100, 7, other); !ok {
		t.Fatal("其他用户应可绑定")
	}
	a.unbindChatGen(1, tokens[0])
	again := &chatGenToken{cancel: func() {}}
	if ok, why := a.tryBindChatGen(99, 42, again); !ok || why != "" {
		t.Fatalf("释放后应可再绑定 why=%s", why)
	}
}

func TestWaitSensitiveDecisionIgnoresUnrelatedCancel(t *testing.T) {
	genCtx, genCancel := context.WithCancel(context.Background())
	defer genCancel()
	clientCtx, clientCancel := context.WithCancel(context.Background())
	clientCancel()
	if clientCtx.Err() == nil {
		t.Fatal("client 应已取消")
	}
	decision := make(chan bool, 1)
	done := make(chan struct{})
	var approved bool
	var msg string
	go func() {
		approved, msg = waitSensitiveDecision(genCtx, decision, time.Second)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("无关 context 取消不得结束审批等待")
	case <-time.After(50 * time.Millisecond):
	}
	decision <- true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("用户确认后应返回")
	}
	if !approved || msg != "" {
		t.Fatalf("approved=%v msg=%q", approved, msg)
	}
}

func TestWaitSensitiveDecisionStop(t *testing.T) {
	genCtx, genCancel := context.WithCancel(context.Background())
	genCancel()
	approved, msg := waitSensitiveDecision(genCtx, make(chan bool), time.Second)
	if approved || msg == "" {
		t.Fatalf("停止应拒绝审批 approved=%v msg=%q", approved, msg)
	}
}

func TestSSEEventCritical(t *testing.T) {
	if !sseEventCritical(`{"done":true,"message_id":1}`) {
		t.Fatal("done 应 critical")
	}
	if !sseEventCritical(`{"error":"x"}`) {
		t.Fatal("error 应 critical")
	}
	if !sseEventCritical(`{"approval":{"id":"a"}}`) {
		t.Fatal("approval 应 critical")
	}
	if sseEventCritical(`{"delta":"hello"}`) {
		t.Fatal("delta 不应 critical")
	}
}

func TestSessionGenerating(t *testing.T) {
	a := &App{}
	if a.sessionGenerating(1) {
		t.Fatal("空闲会话不应 generating")
	}
	tok := &chatGenToken{cancel: func() {}}
	if ok, _ := a.tryBindChatGen(1, 1, tok); !ok {
		t.Fatal("绑定失败")
	}
	if !a.sessionGenerating(1) {
		t.Fatal("绑定后应 generating")
	}
	a.unbindChatGen(1, tok)
	if a.sessionGenerating(1) {
		t.Fatal("解绑后不应 generating")
	}
}
