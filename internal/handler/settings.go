package handler

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"doc-share/internal/model"
	"doc-share/internal/storage"
	"doc-share/internal/util"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// TestMail 发送测试邮件（SMTP 配置连通性验证，仅 admin）
func (a *App) TestMail(c *gin.Context) {
	var req struct {
		To string `json:"to"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	to := strings.TrimSpace(req.To)
	if !util.ValidEmail(to) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "收件地址格式不正确"})
		return
	}
	s := a.Settings()
	if s.SMTPHost == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请先填写并保存 SMTP 服务器配置"})
		return
	}
	subject := s.SiteName + " 邮件服务测试"
	body := "<p>这是一封测试邮件，收到即说明 SMTP 配置正确。</p><p>发送时间：" + time.Now().Format("2006-01-02 15:04:05") + "</p>"
	if err := util.SendMail(s.MailConfig(), to, subject, body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SiteSettings 站点设置（内存缓存，保存时失效）
type SiteSettings struct {
	SiteName   string
	SiteLogo   string
	SiteDomain string

	// 注册方式（单选）：username / email / phone
	RegMethod string

	// SMTP 发信配置（邮箱注册验证码用）
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPFrom     string
	SMTPFromName string // 发件人显示名称（收件箱里显示的名字，空则显示裸地址）
	SMTPSSL      bool

	// 图片存储：local（默认）/ rustfs（S3 兼容）
	StorageDriver    string
	RustFSEndpoint   string
	RustFSRegion     string
	RustFSAccessKey  string
	RustFSSecretKey  string
	RustFSBucket     string
}

// 常用默认值
const defaultSiteName = "DocShare"

// 系统设置键名（注册方式、SMTP、存储）
const (
	keyRegMethod = "reg_method"
	keySMTPHost  = "smtp_host"
	keySMTPPort    = "smtp_port"
	keySMTPUser    = "smtp_user"
	keySMTPPass    = "smtp_pass"
	keySMTPFrom    = "smtp_from"
	keySMTPFromName = "smtp_from_name"
	keySMTPSSL     = "smtp_ssl"

	keyStorageDriver   = "storage_driver"
	keyRustFSEndpoint  = "rustfs_endpoint"
	keyRustFSRegion    = "rustfs_region"
	keyRustFSAccessKey = "rustfs_access_key"
	keyRustFSSecretKey = "rustfs_secret_key"
	keyRustFSBucket    = "rustfs_bucket"
)

func boolVal(s string) bool { return s == "1" || s == "true" }

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Settings 读取站点设置：首次从 DB 加载后缓存，保存时由 UpdateSettings 失效
func (a *App) Settings() SiteSettings {
	a.setMu.RLock()
	if a.setCache != nil {
		s := *a.setCache
		a.setMu.RUnlock()
		return s
	}
	a.setMu.RUnlock()

	s := SiteSettings{
		SiteName:      defaultSiteName,
		RegMethod:     "username", // 默认用户名注册
		SMTPPort:      465,
		SMTPSSL:       true,
		StorageDriver: storage.DriverLocal,
		RustFSRegion:  "us-east-1",
	}
	var rows []model.SystemSetting
	if err := a.DB.Find(&rows).Error; err != nil {
		return s // 查询失败不写缓存，下次请求重试；本次降级为默认值
	}
	for _, r := range rows {
		switch r.Key {
		case model.SettingSiteName:
			if strings.TrimSpace(r.Value) != "" {
				s.SiteName = strings.TrimSpace(r.Value)
			}
		case model.SettingSiteLogo:
			s.SiteLogo = strings.TrimSpace(r.Value)
		case model.SettingSiteDomain:
			s.SiteDomain = strings.TrimSpace(r.Value)
		case keyRegMethod:
			if m := strings.TrimSpace(r.Value); m == "username" || m == "email" || m == "phone" {
				s.RegMethod = m
			}
		case keySMTPHost:
			s.SMTPHost = strings.TrimSpace(r.Value)
		case keySMTPPort:
			if p, err := strconv.Atoi(strings.TrimSpace(r.Value)); err == nil && p > 0 {
				s.SMTPPort = p
			}
		case keySMTPUser:
			s.SMTPUser = strings.TrimSpace(r.Value)
		case keySMTPPass:
			s.SMTPPass = r.Value
		case keySMTPFrom:
			s.SMTPFrom = strings.TrimSpace(r.Value)
		case keySMTPFromName:
			s.SMTPFromName = strings.TrimSpace(r.Value)
		case keySMTPSSL:
			s.SMTPSSL = boolVal(r.Value)
		case keyStorageDriver:
			if d := strings.TrimSpace(r.Value); d == storage.DriverLocal || d == storage.DriverRustFS {
				s.StorageDriver = d
			}
		case keyRustFSEndpoint:
			s.RustFSEndpoint = strings.TrimSpace(r.Value)
		case keyRustFSRegion:
			if v := strings.TrimSpace(r.Value); v != "" {
				s.RustFSRegion = v
			}
		case keyRustFSAccessKey:
			s.RustFSAccessKey = strings.TrimSpace(r.Value)
		case keyRustFSSecretKey:
			s.RustFSSecretKey = r.Value
		case keyRustFSBucket:
			s.RustFSBucket = strings.TrimSpace(r.Value)
		}
	}

	a.setMu.Lock()
	a.setCache = &s
	a.setMu.Unlock()
	return s
}

// MailConfig 当前 SMTP 配置（供验证码发送）
func (s SiteSettings) MailConfig() util.MailConfig {
	return util.MailConfig{
		Host: s.SMTPHost, Port: s.SMTPPort, User: s.SMTPUser,
		Pass: s.SMTPPass, From: s.SMTPFrom, FromName: s.SMTPFromName, SSL: s.SMTPSSL,
	}
}

// SettingsPage 系统设置页（仅 admin）
func (a *App) SettingsPage(c *gin.Context) {
	s := a.Settings()
	a.render(c, "settings.html", gin.H{
		"title": a.tr(c, "set.title"),
		"set":   s,
	})
}

// UpdateSettings 保存站点设置（仅 admin）；logo 走 /admin/api/upload 得到 URL 后填入
func (a *App) UpdateSettings(c *gin.Context) {
	var req struct {
		SiteName   string `json:"site_name"`
		SiteLogo   string `json:"site_logo"`
		SiteDomain string `json:"site_domain"`
		// 注册方式（单选）：username / email / phone
		RegMethod string `json:"reg_method"`
		// SMTP
		SMTPHost     string `json:"smtp_host"`
		SMTPPort     int    `json:"smtp_port"`
		SMTPUser     string `json:"smtp_user"`
		SMTPPass     string `json:"smtp_pass"`
		SMTPFrom     string `json:"smtp_from"`
		SMTPFromName string `json:"smtp_from_name"`
		SMTPSSL      bool   `json:"smtp_ssl"`
		// 存储
		StorageDriver   string `json:"storage_driver"`
		RustFSEndpoint  string `json:"rustfs_endpoint"`
		RustFSRegion    string `json:"rustfs_region"`
		RustFSAccessKey string `json:"rustfs_access_key"`
		RustFSSecretKey string `json:"rustfs_secret_key"`
		RustFSBucket    string `json:"rustfs_bucket"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	name := strings.TrimSpace(req.SiteName)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.errName")})
		return
	}
	if len(name) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "网站名称过长（≤64 字符）"})
		return
	}
	logo := strings.TrimSpace(req.SiteLogo)
	if logo != "" {
		if len(logo) > 500 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Logo 地址过长（≤500 字符）"})
			return
		}
		// 协议白名单：相对路径（/uploads/...）或 http(s)；防止存入 javascript: 等伪协议
		low := strings.ToLower(logo)
		if !strings.HasPrefix(low, "/") && !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Logo 地址仅支持站内路径或 http(s) 链接"})
			return
		}
	}
	domain := normalizeSiteURL(req.SiteDomain)
	if strings.TrimSpace(req.SiteDomain) != "" && domain == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.errDomain")})
		return
	}
	if len(domain) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.errDomain")})
		return
	}

	// 注册方式单选校验
	if req.RegMethod != "username" && req.RegMethod != "email" && req.RegMethod != "phone" {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.errRegMethod")})
		return
	}
	port := req.SMTPPort
	if port <= 0 || port > 65535 {
		port = 465
	}

	driver := strings.TrimSpace(req.StorageDriver)
	if driver == "" {
		driver = storage.DriverLocal
	}
	if driver != storage.DriverLocal && driver != storage.DriverRustFS {
		c.JSON(http.StatusBadRequest, gin.H{"error": a.tr(c, "set.errStorageDriver")})
		return
	}
	endpoint := strings.TrimRight(strings.TrimSpace(req.RustFSEndpoint), "/")
	region := strings.TrimSpace(req.RustFSRegion)
	if region == "" {
		region = "us-east-1"
	}
	accessKey := strings.TrimSpace(req.RustFSAccessKey)
	bucket := strings.TrimSpace(req.RustFSBucket)
	secretKey := req.RustFSSecretKey
	// Secret 留空：保留库中原值（与常见密码表单一致，避免每次保存被迫重填）
	if strings.TrimSpace(secretKey) == "" {
		secretKey = a.Settings().RustFSSecretKey
	}
	if driver == storage.DriverRustFS {
		if _, err := (storage.RustFSConfig{
			Endpoint: endpoint, Region: region, AccessKey: accessKey,
			SecretKey: secretKey, Bucket: bucket,
		}).Validate(); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	values := map[string]string{
		model.SettingSiteName:   name,
		model.SettingSiteLogo:   logo,
		model.SettingSiteDomain: domain,
		keyRegMethod:            req.RegMethod,
		keySMTPHost:             strings.TrimSpace(req.SMTPHost),
		keySMTPPort:             strconv.Itoa(port),
		keySMTPUser:             strings.TrimSpace(req.SMTPUser),
		keySMTPPass:             req.SMTPPass,
		keySMTPFrom:             strings.TrimSpace(req.SMTPFrom),
		keySMTPFromName:         strings.TrimSpace(req.SMTPFromName),
		keySMTPSSL:              boolStr(req.SMTPSSL),
		keyStorageDriver:        driver,
		keyRustFSEndpoint:       endpoint,
		keyRustFSRegion:         region,
		keyRustFSAccessKey:      accessKey,
		keyRustFSSecretKey:      secretKey,
		keyRustFSBucket:         bucket,
	}
	// 单条批量 upsert（INSERT ... ON DUPLICATE KEY UPDATE）：
	// 一次网络往返写完全部设置。逐条 SELECT+UPDATE 在远程 MySQL 上要 2N 次往返，保存明显变慢
	rows := make([]model.SystemSetting, 0, len(values))
	for k, v := range values {
		rows = append(rows, model.SystemSetting{Key: k, Value: v})
	}
	err := a.DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(&rows).Error
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存失败"})
		return
	}

	a.setMu.Lock()
	a.setCache = nil
	a.setMu.Unlock()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// TestRustFS 测试 RustFS 连通性（凭证 + Bucket）；可用表单当前值或已保存配置
func (a *App) TestRustFS(c *gin.Context) {
	var req struct {
		Endpoint  string `json:"rustfs_endpoint"`
		Region    string `json:"rustfs_region"`
		AccessKey string `json:"rustfs_access_key"`
		SecretKey string `json:"rustfs_secret_key"`
		Bucket    string `json:"rustfs_bucket"`
	}
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误"})
		return
	}
	saved := a.Settings()
	secret := req.SecretKey
	if strings.TrimSpace(secret) == "" {
		secret = saved.RustFSSecretKey
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		endpoint = saved.RustFSEndpoint
	}
	region := strings.TrimSpace(req.Region)
	if region == "" {
		region = saved.RustFSRegion
	}
	accessKey := strings.TrimSpace(req.AccessKey)
	if accessKey == "" {
		accessKey = saved.RustFSAccessKey
	}
	bucket := strings.TrimSpace(req.Bucket)
	if bucket == "" {
		bucket = saved.RustFSBucket
	}
	backend, err := storage.NewRustFS(storage.RustFSConfig{
		Endpoint: endpoint, Region: region, AccessKey: accessKey,
		SecretKey: secret, Bucket: bucket,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx := c.Request.Context()
	if err := backend.Ping(ctx); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// fileStorage 按当前站点设置构造上传后端
func (a *App) fileStorage() (storage.Storage, error) {
	s := a.Settings()
	switch s.StorageDriver {
	case storage.DriverRustFS:
		return storage.NewRustFS(storage.RustFSConfig{
			Endpoint:  s.RustFSEndpoint,
			Region:    s.RustFSRegion,
			AccessKey: s.RustFSAccessKey,
			SecretKey: s.RustFSSecretKey,
			Bucket:    s.RustFSBucket,
		})
	default:
		return &storage.Local{Dir: a.Cfg.Upload.Dir}, nil
	}
}

// normalizeSiteURL 归一域名：补 https:// 前缀、去尾部斜杠；空值返回空，
// 无法解析出合法 http(s) host 时返回空（由调用方判为格式错误）
func normalizeSiteURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

// siteBaseURL 分享链接等绝对地址的站点前缀：优先系统域名，留空取当前请求
func (a *App) siteBaseURL(c *gin.Context) string {
	if d := a.Settings().SiteDomain; d != "" {
		return d
	}
	return scheme(c) + "://" + c.Request.Host
}
