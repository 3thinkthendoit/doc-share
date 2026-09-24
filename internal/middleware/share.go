package middleware

import (
	"sync"
	"time"
)

// ShareLimiter 分享密码错误次数限制（内存计数，按 IP+token）
type ShareLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attempt
	max      int
	window   time.Duration
}

type attempt struct {
	count   int
	expires time.Time
}

func NewShareLimiter(max int, window time.Duration) *ShareLimiter {
	l := &ShareLimiter{
		attempts: make(map[string]*attempt),
		max:      max,
		window:   window,
	}
	go l.gc()
	return l
}

func key(ip, token string) string { return ip + "|" + token }

// Allowed 判断当前是否仍允许尝试
func (l *ShareLimiter) Allowed(ip, token string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key(ip, token)]
	if !ok || time.Now().After(a.expires) {
		return true
	}
	return a.count < l.max
}

// Fail 记录一次失败
func (l *ShareLimiter) Fail(ip, token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := key(ip, token)
	a, ok := l.attempts[k]
	if !ok || time.Now().After(a.expires) {
		l.attempts[k] = &attempt{count: 1, expires: time.Now().Add(l.window)}
		return
	}
	a.count++
}

// Reset 验证成功后清空计数
func (l *ShareLimiter) Reset(ip, token string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key(ip, token))
}

func (l *ShareLimiter) gc() {
	for {
		time.Sleep(time.Minute)
		now := time.Now()
		l.mu.Lock()
		for k, a := range l.attempts {
			if now.After(a.expires) {
				delete(l.attempts, k)
			}
		}
		l.mu.Unlock()
	}
}
