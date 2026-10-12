package storage

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// COS S3 兼容对象存储（腾讯云 COS）
type COS struct {
	cfg    COSConfig
	client *s3.Client
}

// NewCOS 构造客户端；调用前须 Validate 配置
func NewCOS(cfg COSConfig) (*COS, error) {
	cfg, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: aws.NewCredentialsCache(
			credentials.NewStaticCredentialsProvider(cfg.SecretID, cfg.SecretKey, ""),
		),
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
		// 与 RustFS 一致，避免强制校验头
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &COS{cfg: cfg, client: client}, nil
}

func (c *COS) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (PutResult, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	key = strings.TrimLeft(key, "/")
	in := &s3.PutObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if ct := strings.TrimSpace(contentType); ct != "" {
		in.ContentType = aws.String(ct)
	}
	if _, err := c.client.PutObject(ctx, in); err != nil {
		return PutResult{}, fmt.Errorf("COS 上传失败: %w", err)
	}
	return PutResult{URL: ObjectURL(c.cfg.Endpoint, c.cfg.Bucket, key)}, nil
}

// Get 拉取对象流；不额外加超时，读取随请求 ctx 取消（大文件流式转发）
func (c *COS) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.Contains(key, "..") {
		return nil, 0, fmt.Errorf("非法存储 key")
	}
	out, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("COS 读取失败: %w", err)
	}
	return out.Body, aws.ToInt64(out.ContentLength), nil
}

func (c *COS) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	key = strings.TrimLeft(key, "/")
	if key == "" || strings.Contains(key, "..") {
		return fmt.Errorf("非法存储 key")
	}
	_, err := c.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("COS 删除失败: %w", err)
	}
	return nil
}

func (c *COS) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	_, err := c.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(c.cfg.Bucket),
	})
	if err != nil {
		return fmt.Errorf("无法访问 Bucket %q: %w", c.cfg.Bucket, err)
	}
	return nil
}
