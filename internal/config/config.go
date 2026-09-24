package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// placeholderPattern 匹配三种占位符（默认值不允许跨行、不含 '}'）：
//
//	${VAR}       未设置或为空时展开为空字符串（回报 Issue）
//	${VAR:-def}  未设置或为空时使用默认值 def
//	${VAR-def}   仅未设置时使用默认值 def（显式设为空则展开为空）
var placeholderPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-|-)?([^}\n]*)\}`)

// placeholderSecret 是公开在仓库中的开发占位密钥，禁止在生产使用
const placeholderSecret = "please-change-this-to-a-long-random-secret"

// Issue 描述加载配置时发现的情况（如变量未设置、使用了默认值），由调用方决定如何输出。
type Issue struct {
	Key    string // 配置键路径，如 database.dsn
	Env    string // 环境变量名
	Reason string
}

// Config 应用全局配置
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Auth     AuthConfig     `yaml:"auth"`
	Upload   UploadConfig   `yaml:"upload"`
}

type ServerConfig struct {
	Port string `yaml:"port"` // 监听端口，如 :8080
	Dev  bool   `yaml:"dev"`  // 开发模式：模板/静态资源直读磁盘，改前端无需重启
}

type DatabaseConfig struct {
	DSN         string `yaml:"dsn"` // MySQL DSN
	ShowLog     bool   `yaml:"show_log"`
	MaxOpenConn int    `yaml:"max_open_conn"`
	MaxIdleConn int    `yaml:"max_idle_conn"`
}

type AuthConfig struct {
	SessionSecret   string `yaml:"session_secret"`   // session/HMAC 签名密钥
	DefaultAdmin    string `yaml:"default_admin"`    // 首次启动创建的 admin 用户名
	DefaultPassword string `yaml:"default_password"` // 首次启动创建的 admin 密码
}

// UploadConfig 图片上传存储配置
type UploadConfig struct {
	Dir       string `yaml:"dir"`         // 本地存储根目录
	MaxSizeMB int    `yaml:"max_size_mb"` // 单张图片大小上限（MB）
}

// Load 从 YAML 文件加载配置。
// 占位符在 YAML 解析之后逐标量展开（而非文本级替换），因此注释不受影响，
// 值中的引号/反斜杠/换行不会破坏文档结构，也不存在跨行结构注入。
// 返回的 issues 供调用方打印（如使用了默认值、变量未设置）。
func Load(path string) (*Config, []Issue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("解析 YAML 失败: %w", err)
	}
	var issues []Issue
	if err := expandNode(&doc, "", &issues); err != nil {
		return nil, issues, err
	}
	expanded, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, issues, err
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(expanded, cfg); err != nil {
		return nil, issues, fmt.Errorf("配置字段类型非法（布尔仅接受 true/false，整数需为数字，请检查环境变量取值）: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, issues, err
	}
	return cfg, issues, nil
}

// expandNode 递归遍历 YAML 节点，对包含占位符的标量值做展开。
// keyPath 用于在告警与错误中定位具体配置项。
func expandNode(n *yaml.Node, keyPath string, issues *[]Issue) error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := expandNode(c, keyPath, issues); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		// Content 形如 [k1, v1, k2, v2, ...]，只展开值节点
		for i := 0; i+1 < len(n.Content); i += 2 {
			childPath := n.Content[i].Value
			if keyPath != "" {
				childPath = keyPath + "." + childPath
			}
			if err := expandNode(n.Content[i+1], childPath, issues); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			if err := expandNode(c, fmt.Sprintf("%s[%d]", keyPath, i), issues); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "${") {
			return nil
		}
		val, got, err := expandString(n.Value, keyPath)
		if err != nil {
			return err
		}
		if val != n.Value {
			n.Value = val
			// 清除原始引号/类型标记，让 yaml 按展开后的值重新推断（如 bool/int）
			n.Style = 0
			n.Tag = ""
		}
		*issues = append(*issues, got...)
	}
	return nil
}

// expandString 展开单个标量值中的占位符；检测未闭合的 ${ 并报错；返回去重后的 Issue。
func expandString(s, key string) (string, []Issue, error) {
	var issues []Issue
	seen := make(map[string]bool)
	report := func(env, reason string) {
		id := key + "|" + env + "|" + reason
		if seen[id] {
			return
		}
		seen[id] = true
		issues = append(issues, Issue{Key: key, Env: env, Reason: reason})
	}
	result := placeholderPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := placeholderPattern.FindStringSubmatch(m)
		name, op, def := sub[1], sub[2], sub[3]
		v, set := os.LookupEnv(name)
		switch {
		case set && v != "":
			return v
		case op == ":-":
			report(name, "未设置或为空，使用默认值")
			return def
		case op == "-":
			if set {
				return "" // 显式设为空，${VAR-d} 不回退默认值
			}
			report(name, "未设置，使用默认值")
			return def
		default:
			report(name, "未设置或为空，已展开为空")
			return ""
		}
	})
	if i := strings.Index(result, "${"); i >= 0 {
		return "", nil, fmt.Errorf("配置项 %q 存在无法解析的占位符（未闭合或默认值含 '}'）: %.40s...", key, s)
	}
	return result, issues, nil
}

// Load 后校验敏感项。session_secret 缺失或仍为占位值时拒绝启动，
// 本地调试可设 DOC_SHARE_ALLOW_INSECURE_DEFAULTS=true 放行。
func (c *Config) validate() error {
	allowInsecure := os.Getenv("DOC_SHARE_ALLOW_INSECURE_DEFAULTS") == "true"
	if c.Auth.SessionSecret == "" {
		if !allowInsecure {
			return fmt.Errorf("auth.session_secret 未配置（请设置环境变量 DOC_SHARE_SESSION_SECRET）；本地调试可设 DOC_SHARE_ALLOW_INSECURE_DEFAULTS=true")
		}
		c.Auth.SessionSecret = placeholderSecret
		return nil
	}
	if c.Auth.SessionSecret == placeholderSecret || len(c.Auth.SessionSecret) < 32 {
		if !allowInsecure {
			return fmt.Errorf("auth.session_secret 为公开占位值或长度不足 32，存在会话伪造风险；请配置随机长密钥，本地调试可设 DOC_SHARE_ALLOW_INSECURE_DEFAULTS=true")
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Server.Port == "" {
		c.Server.Port = ":8080"
	} else if !strings.Contains(c.Server.Port, ":") {
		c.Server.Port = ":" + c.Server.Port // 容忍 DOC_SHARE_PORT=8080 写法
	}
	if c.Auth.DefaultAdmin == "" {
		c.Auth.DefaultAdmin = "admin"
	}
	if c.Auth.DefaultPassword == "" {
		c.Auth.DefaultPassword = "admin123"
	}
	if c.Database.MaxOpenConn <= 0 {
		c.Database.MaxOpenConn = 50
	}
	if c.Database.MaxIdleConn <= 0 {
		c.Database.MaxIdleConn = 10
	}
	if c.Upload.Dir == "" {
		c.Upload.Dir = "./uploads"
	}
	if c.Upload.MaxSizeMB <= 0 {
		c.Upload.MaxSizeMB = 10
	}
}
