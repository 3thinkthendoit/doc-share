package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Local 本地磁盘存储：根目录映射到站内 /uploads
type Local struct {
	Dir string // 本地根目录，如 ./uploads
}

func (l *Local) Put(ctx context.Context, key string, body io.Reader, _ int64, _ string) (PutResult, error) {
	if err := ctx.Err(); err != nil {
		return PutResult{}, err
	}
	key = stringsCleanKey(key)
	dst := filepath.Join(l.Dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return PutResult{}, fmt.Errorf("创建存储目录失败: %w", err)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return PutResult{}, fmt.Errorf("创建文件失败: %w", err)
	}
	_, copyErr := io.Copy(f, body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dst) // 写入失败不留半截文件
		return PutResult{}, fmt.Errorf("写入文件失败: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return PutResult{}, fmt.Errorf("写入文件失败: %w", closeErr)
	}
	return PutResult{URL: "/uploads/" + key}, nil
}

func (l *Local) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key = stringsCleanKey(key)
	if key == "" || strings.Contains(key, "..") {
		return fmt.Errorf("非法存储 key")
	}
	absDir, err := filepath.Abs(l.Dir)
	if err != nil {
		return fmt.Errorf("解析上传目录失败: %w", err)
	}
	dst := filepath.Join(absDir, filepath.FromSlash(key))
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return fmt.Errorf("解析目标路径失败: %w", err)
	}
	sep := string(os.PathSeparator)
	if absDst != absDir && !strings.HasPrefix(absDst, absDir+sep) {
		return fmt.Errorf("非法存储 key")
	}
	if err := os.Remove(absDst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除文件失败: %w", err)
	}
	return nil
}

func (l *Local) Ping(_ context.Context) error {
	if l.Dir == "" {
		return fmt.Errorf("本地上传目录未配置")
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return fmt.Errorf("本地上传目录不可写: %w", err)
	}
	return nil
}

func stringsCleanKey(key string) string {
	key = filepath.ToSlash(key)
	return strings.TrimLeft(key, "/")
}
