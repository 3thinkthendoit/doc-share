// Package storage 抽象图片上传后端：本地磁盘与 S3 兼容对象存储（RustFS）。
package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
)

// Driver 存储驱动名（与系统设置 storage_driver 一致）
const (
	DriverLocal  = "local"
	DriverRustFS = "rustfs"
)

// opTimeout 单次对象存储操作超时（上传 / 连通性检测）
const opTimeout = 15 * time.Second


// PutResult 上传成功后的对外可访问 URL
type PutResult struct {
	URL string
}

// Storage 图片存储后端
type Storage interface {
	// Put 写入对象；key 形如 202609/abc.png；contentType 可为空
	Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (PutResult, error)
	// Delete 删除对象；key 不存在视为成功
	Delete(ctx context.Context, key string) error
	// Ping 连通性检测（凭证、endpoint、bucket）
	Ping(ctx context.Context) error
}

// RustFSConfig S3 兼容连接参数（RustFS）
type RustFSConfig struct {
	Endpoint  string // 如 http://localhost:9000
	Region    string // 默认 us-east-1
	AccessKey string
	SecretKey string
	Bucket    string
}

// Validate 校验必填项；返回规范化后的配置副本
func (c RustFSConfig) Validate() (RustFSConfig, error) {
	out := RustFSConfig{
		Endpoint:  strings.TrimRight(strings.TrimSpace(c.Endpoint), "/"),
		Region:    strings.TrimSpace(c.Region),
		AccessKey: strings.TrimSpace(c.AccessKey),
		SecretKey: c.SecretKey, // 密钥可能含首尾空格，不去 Trim
		Bucket:    strings.TrimSpace(c.Bucket),
	}
	if out.Region == "" {
		out.Region = "us-east-1"
	}
	if out.Endpoint == "" {
		return out, fmt.Errorf("RustFS Endpoint 不能为空")
	}
	if !strings.HasPrefix(out.Endpoint, "http://") && !strings.HasPrefix(out.Endpoint, "https://") {
		return out, fmt.Errorf("RustFS Endpoint 须以 http:// 或 https:// 开头")
	}
	if out.AccessKey == "" {
		return out, fmt.Errorf("RustFS Access Key 不能为空")
	}
	if out.SecretKey == "" {
		return out, fmt.Errorf("RustFS Secret Key 不能为空")
	}
	if out.Bucket == "" {
		return out, fmt.Errorf("RustFS Bucket 不能为空")
	}
	return out, nil
}

// ObjectURL path-style 直链：{endpoint}/{bucket}/{key}
func ObjectURL(endpoint, bucket, key string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	key = strings.TrimLeft(key, "/")
	return endpoint + "/" + bucket + "/" + key
}

// uploadKeyRe 普通上传 key：YYYYMM/slug.ext
var uploadKeyRe = regexp.MustCompile(`(?i)^[0-9]{6}/[a-zA-Z0-9]+\.(png|jpe?g|gif|webp|bmp)$`)

// embedUploadKeyRe 嵌入预览专用 key：embed/YYYYMM/slug.ext（仅此类允许被 replace/DELETE 清理）
var embedUploadKeyRe = regexp.MustCompile(`(?i)^embed/[0-9]{6}/[a-zA-Z0-9]+\.(png|jpe?g|gif|webp|bmp)$`)

func cleanUploadKey(key string) string {
	return strings.TrimLeft(strings.ReplaceAll(key, "\\", "/"), "/")
}

// ValidUploadKey 校验可解析的存储 key（普通图 + 嵌入预览），防止路径穿越
func ValidUploadKey(key string) bool {
	key = cleanUploadKey(key)
	if key == "" || strings.Contains(key, "..") || strings.Contains(key, "//") {
		return false
	}
	return uploadKeyRe.MatchString(key) || embedUploadKeyRe.MatchString(key)
}

// ValidEmbedUploadKey 仅嵌入预览对象；异步删除/覆盖替换只允许这类 key
func ValidEmbedUploadKey(key string) bool {
	key = cleanUploadKey(key)
	if key == "" || strings.Contains(key, "..") || strings.Contains(key, "//") {
		return false
	}
	return embedUploadKeyRe.MatchString(key)
}

// ObjectKeyFromURL 从对外 URL 解析存储 key；无法识别返回空字符串。
// 支持：/uploads/{key}、带站点前缀的 /uploads/{key}、RustFS ObjectURL。
func ObjectKeyFromURL(rawURL string, rust *RustFSConfig) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if strings.HasPrefix(rawURL, "/uploads/") {
		key := strings.TrimPrefix(rawURL, "/uploads/")
		if q := strings.IndexByte(key, '?'); q >= 0 {
			key = key[:q]
		}
		if ValidUploadKey(key) {
			return key
		}
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	p := path.Clean("/" + strings.TrimPrefix(u.Path, "/"))
	if strings.HasPrefix(p, "/uploads/") {
		key := strings.TrimPrefix(p, "/uploads/")
		if ValidUploadKey(key) {
			return key
		}
		return ""
	}
	if rust == nil {
		return ""
	}
	cfg, err := rust.Validate()
	if err != nil {
		return ""
	}
	prefix := ObjectURL(cfg.Endpoint, cfg.Bucket, "")
	// prefix 形如 http://host/bucket/ ；兼容无尾斜杠比较
	candidates := []string{rawURL, strings.TrimRight(rawURL, "/")}
	for _, c := range candidates {
		if strings.HasPrefix(c, prefix) {
			key := strings.TrimPrefix(c, prefix)
			if q := strings.IndexByte(key, '?'); q >= 0 {
				key = key[:q]
			}
			key = strings.TrimLeft(key, "/")
			if ValidUploadKey(key) {
				return key
			}
		}
	}
	return ""
}
