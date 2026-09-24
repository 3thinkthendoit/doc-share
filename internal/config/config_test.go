package config

import (
	"os"
	"strings"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExpandString(t *testing.T) {
	t.Setenv("DS_TEST_VAR", "hello")
	t.Setenv("DS_TEST_BLANK", "")

	got, issues, err := expandString(
		"a=${DS_TEST_VAR};b=${DS_TEST_MISSING:-fallback};c=${DS_TEST_BARE};d=${DS_TEST_BLANK:-dft};e=${DS_TEST_BLANK-fallback};f=${DS_TEST_MISSING-fallback}",
		"test.key")
	if err != nil {
		t.Fatal(err)
	}
	want := "a=hello;b=fallback;c=;d=dft;e=;f=fallback"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// ${VAR-d} 在变量显式为空时不回退（e=），仅未设置时回退（f=）
	var bareWarn, dashColon, dash bool
	for _, is := range issues {
		switch is.Env {
		case "DS_TEST_BARE":
			bareWarn = is.Key == "test.key"
		case "DS_TEST_MISSING":
			dashColon = true
		case "DS_TEST_MISSING-fallback":
		default:
		}
		if is.Env == "DS_TEST_MISSING" && strings.Contains(is.Reason, "默认值") {
			dash = true
		}
	}
	if !bareWarn || !dashColon || !dash {
		t.Errorf("issues=%v, 期望包含 bare 警告与两处默认值回退提示", issues)
	}
}

func TestExpandStringUnclosedPlaceholder(t *testing.T) {
	_, _, err := expandString("oops ${UNCLOSED value", "database.dsn")
	if err == nil || !strings.Contains(err.Error(), "database.dsn") {
		t.Errorf("期望报出含键路径的错误, got %v", err)
	}
}

func TestLoadWithEnv(t *testing.T) {
	path := writeFile(t, `
server:
  port: "${DS_TEST_PORT:-:9090}"
database:
  show_log: ${DS_TEST_LOG:-false}
  max_open_conn: ${DS_TEST_MAX:-50}
auth:
  session_secret: "${DS_TEST_SECRET}"
  default_admin: "${DS_TEST_MISSING:-admin}"
`)
	t.Setenv("DS_TEST_SECRET", "s3cret-value-that-is-long-enough-xx")

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != ":9090" {
		t.Errorf("port=%q, want :9090", cfg.Server.Port)
	}
	if cfg.Database.ShowLog || cfg.Database.MaxOpenConn != 50 {
		t.Errorf("bool/int 默认值未正确解析: %+v", cfg.Database)
	}
	if cfg.Auth.SessionSecret != "s3cret-value-that-is-long-enough-xx" {
		t.Errorf("secret=%q", cfg.Auth.SessionSecret)
	}
	if cfg.Auth.DefaultAdmin != "admin" {
		t.Errorf("admin=%q, want admin", cfg.Auth.DefaultAdmin)
	}
}

func TestLoadBoolFromEnv(t *testing.T) {
	path := writeFile(t, "database:\n  show_log: ${DS_TEST_LOG:-false}\nauth:\n  session_secret: \"${DS_TEST_SECRET3}\"\n")
	t.Setenv("DS_TEST_LOG", "true")
	t.Setenv("DS_TEST_SECRET3", "long-enough-secret-value-for-bool-test")
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Database.ShowLog {
		t.Error("环境变量展开的布尔值应为 true")
	}
}

// 回归 H1：注释中的占位符不应被展开，也不应产生告警
func TestCommentNotExpanded(t *testing.T) {
	path := writeFile(t, "# 示例 ${DS_NOT_A_REAL_VAR:-xyz} 和 ${DS_ANOTHER}\nauth:\n  session_secret: \"${DS_TEST_SECRET2}\"\n")
	t.Setenv("DS_TEST_SECRET2", "another-long-enough-secret-value-1234")
	cfg, issues, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.SessionSecret != "another-long-enough-secret-value-1234" {
		t.Errorf("secret=%q", cfg.Auth.SessionSecret)
	}
	for _, is := range issues {
		if strings.Contains(is.Env, "NOT_A_REAL") || strings.Contains(is.Env, "ANOTHER") {
			t.Errorf("注释中的占位符不应产生 issue: %+v", is)
		}
	}
}

// 回归 H1：值含引号/反斜杠不破坏解析；多行注入值不会污染其他配置项
func TestNoStructuralInjection(t *testing.T) {
	path := writeFile(t, `
database:
  dsn: "${DS_INJ_DSN}"
auth:
  session_secret: "${DS_INJ_SECRET}"
  default_admin: "${DS_INJ_ADMIN:-admin}"
`)
	t.Setenv("DS_INJ_SECRET", "long-enough-secret-for-validation-1234")
	// 典型事故场景：从 vault/k8s 复制的密码带引号、反斜杠、结尾换行
	t.Setenv("DS_INJ_DSN", `user:p"ass\C1@tcp(host:3306)/db`)
	// 恶意/意外多行值：期望只作为字符串进入 dsn，不改变其他字段
	t.Setenv("DS_INJ_ADMIN", "admin\nauth:\n  session_secret: evil")

	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("含特殊字符的值应正常加载: %v", err)
	}
	if cfg.Database.DSN != `user:p"ass\C1@tcp(host:3306)/db` {
		t.Errorf("dsn 被篡改: %q", cfg.Database.DSN)
	}
	if cfg.Auth.SessionSecret != "long-enough-secret-for-validation-1234" {
		t.Errorf("session_secret 被跨行注入污染: %q", cfg.Auth.SessionSecret)
	}
}

// 回归 H2：secret 为占位值/过短/未设置时拒绝启动；显式放行开关生效
func TestSecretValidation(t *testing.T) {
	longOK := "this-is-a-long-random-secret-value-32+"
	cases := []struct {
		name      string
		secretEnv string // "" 表示不设置
		allowEnv  string
		wantErr   bool
		wantEq    string // 期望最终 secret
	}{
		{"未设置且不放行", "", "", true, ""},
		{"未设置但放行", "", "true", false, placeholderSecret},
		{"公开占位值", placeholderSecret, "", true, ""},
		{"长度不足", "short", "", true, ""},
		{"合规长密钥", longOK, "", false, longOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.secretEnv == "" {
				t.Setenv("DOC_SHARE_SESSION_SECRET", "")
			} else {
				t.Setenv("DOC_SHARE_SESSION_SECRET", c.secretEnv)
			}
			t.Setenv("DOC_SHARE_ALLOW_INSECURE_DEFAULTS", c.allowEnv)
			cfg, _, err := Load(writeFile(t, "auth:\n  session_secret: \"${DOC_SHARE_SESSION_SECRET}\"\n"))
			if c.wantErr {
				if err == nil {
					t.Fatal("期望拒绝启动")
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if cfg.Auth.SessionSecret != c.wantEq {
				t.Errorf("secret=%q, want %q", cfg.Auth.SessionSecret, c.wantEq)
			}
		})
	}
}

// 回归 L2：端口缺少冒号时自动归一化
func TestPortNormalize(t *testing.T) {
	t.Setenv("DOC_SHARE_SESSION_SECRET", "long-enough-secret-value-for-test-12345")
	cfg, _, err := Load(writeFile(t, "server:\n  port: \"${DS_P:-8080}\"\nauth:\n  session_secret: \"${DOC_SHARE_SESSION_SECRET}\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != ":8080" {
		t.Errorf("port=%q, want :8080", cfg.Server.Port)
	}
}
