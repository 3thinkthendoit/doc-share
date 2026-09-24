package util

import (
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// MailConfig SMTP 发信配置（host/port/user/pass/from 由管理员在系统设置维护）
type MailConfig struct {
	Host     string
	Port     int
	User     string // 发件账号（SMTP 登录名）；为空则不认证（一般仅内网中继）
	Pass     string // 密码/授权码
	From     string // 发件人地址（显示的发件邮箱）
	FromName string // 发件人显示名称（如「DocShare」，收件箱里展示的名字；空则显示裸地址）
	SSL      bool   // true=465 隐式 TLS；false=25/587（服务端支持时自动升级 STARTTLS）
}

// loginAuth 实现 AUTH LOGIN 机制（标准库未提供；163 等服务商主推该机制）
type loginAuth struct {
	user, pass string
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (proto string, toServer []byte, err error) {
	return "LOGIN", []byte(a.user), nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	prompt := strings.ToLower(string(fromServer))
	if strings.Contains(prompt, "username") || strings.Contains(prompt, "用户名") {
		return []byte(a.user), nil
	}
	if strings.Contains(prompt, "password") || strings.Contains(prompt, "密码") {
		return []byte(a.pass), nil
	}
	return nil, fmt.Errorf("未知的服务端提示：%s", fromServer)
}

// SendMail 发送一封 HTML 邮件（验证码等通知）
func SendMail(cfg MailConfig, to, subject, htmlBody string) error {
	if cfg.Host == "" || cfg.Port <= 0 {
		return fmt.Errorf("SMTP 未配置")
	}
	if cfg.From == "" {
		cfg.From = cfg.User
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	var conn net.Conn
	var err error
	if cfg.SSL {
		conn, err = tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
	} else {
		conn, err = net.DialTimeout("tcp", addr, 10*time.Second)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 失败：%w", err)
	}
	// 整体会话 20 秒超时：SMTP 服务端挂起时不至于永久占用请求 goroutine
	if err := conn.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
		conn.Close()
		return fmt.Errorf("设置超时失败：%w", err)
	}

	cl, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP 握手失败：%w", err)
	}
	defer cl.Close()

	// 明文端口若服务端支持 STARTTLS 则升级，保证认证凭据不裸奔
	if !cfg.SSL {
		if ok, _ := cl.Extension("STARTTLS"); ok {
			if err := cl.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("STARTTLS 失败：%w", err)
			}
		}
	}

	if cfg.User != "" {
		if ok, _ := cl.Extension("AUTH"); ok {
			if err := cl.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
				// 部分服务商（如 163）对 AUTH PLAIN 兼容性差，回退 AUTH LOGIN 重试
				if err2 := cl.Auth(&loginAuth{user: cfg.User, pass: cfg.Pass}); err2 != nil {
					return fmt.Errorf("SMTP 认证失败：%w", err)
				}
			}
		}
	}

	// 信封发件人（MAIL FROM）必须等于认证账号——163 等服务商严格校验一致，
	// 否则 553；显示用发件人（From 头）仍可用别名/显示名
	envelopeFrom := cfg.From
	if cfg.User != "" && cfg.From != cfg.User {
		envelopeFrom = cfg.User
	}
	if err := cl.Mail(envelopeFrom); err != nil {
		return fmt.Errorf("MAIL FROM 失败：%w", err)
	}
	if err := cl.Rcpt(to); err != nil {
		return fmt.Errorf("收件人被拒绝：%w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败：%w", err)
	}

	// 发件人：有显示名称时输出「名称 <地址>」，中文按 RFC 2047 编码
	from := cfg.From
	if cfg.FromName != "" {
		from = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("UTF-8", cfg.FromName), cfg.From)
	}
	headers := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n",
		from, to, mime.QEncoding.Encode("UTF-8", subject), time.Now().Format(time.RFC1123Z))
	if _, err := w.Write([]byte(headers + htmlBody)); err != nil {
		w.Close()
		return fmt.Errorf("写邮件正文失败：%w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("结束正文失败：%w", err)
	}
	return cl.Quit()
}

// ValidEmail 宽松邮箱格式校验
func ValidEmail(s string) bool {
	at := strings.Index(s, "@")
	return at > 0 && at < len(s)-1 && !strings.ContainsAny(s, " \t\r\n") && strings.Contains(s[at:], ".")
}

// ValidPhone 中国大陆手机号校验
func ValidPhone(s string) bool {
	if len(s) != 11 || s[0] != '1' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s[1] >= '3' // 12/10/11x 开头号段不存在，放宽为第二位 3-9
}
