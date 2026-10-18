package export

import "context"

// runWithContext 在独立 goroutine 执行 fn。
// ctx 取消时仍等待 fn 结束，避免调用方提前释放并发槽或删除临时目录。
// 若 fn 已返回错误则优先返回该错误，否则返回 ctx.Err()。
func runWithContext(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- fn()
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		err := <-errCh // 必须等完，防止 workdir/槽竞态
		if err != nil {
			return err
		}
		return ctx.Err()
	}
}
