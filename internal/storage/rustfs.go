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

// RustFS S3 兼容对象存储（path-style）
type RustFS struct {
	cfg    RustFSConfig
	client *s3.Client
}

// NewRustFS 构造客户端；调用前须 Validate 配置
func NewRustFS(cfg RustFSConfig) (*RustFS, error) {
	cfg, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: aws.NewCredentialsCache(
			credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		),
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
		// 默认 when_supported 会强制附加 CRC32 等校验头，多数 S3 兼容端（含 RustFS）会拒收
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
	})
	return &RustFS{cfg: cfg, client: client}, nil
}

func (r *RustFS) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) (PutResult, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	key = strings.TrimLeft(key, "/")
	in := &s3.PutObjectInput{
		Bucket: aws.String(r.cfg.Bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if size >= 0 {
		in.ContentLength = aws.Int64(size)
	}
	if ct := strings.TrimSpace(contentType); ct != "" {
		in.ContentType = aws.String(ct)
	}
	if _, err := r.client.PutObject(ctx, in); err != nil {
		return PutResult{}, fmt.Errorf("RustFS 上传失败: %w", err)
	}
	return PutResult{URL: ObjectURL(r.cfg.Endpoint, r.cfg.Bucket, key)}, nil
}

func (r *RustFS) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	_, err := r.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(r.cfg.Bucket),
	})
	if err != nil {
		return fmt.Errorf("无法访问 Bucket %q: %w", r.cfg.Bucket, err)
	}
	return nil
}
