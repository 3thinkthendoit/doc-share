// Package storage 抽象图片上传后端：本地磁盘与 S3 兼容对象存储（RustFS）。
package storage

import (
	"context"
	"fmt"
	"io"
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
