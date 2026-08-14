package resource

import (
	"context"
	"fmt"
	"io"
	"sync"
)

type installBudget struct {
	mu          sync.Mutex
	limit       int64
	used        int64
	limitError  error
	description string
}

func newInstallBudget(limit int64) *installBudget {
	return &installBudget{limit: limit, limitError: ErrInstallSizeLimit, description: "installation output"}
}

func newDownloadBudget(limit int64) *installBudget {
	return &installBudget{limit: limit, limitError: ErrDownloadLimit, description: "transaction downloads"}
}

func (b *installBudget) consume(bytes int64) error {
	if b == nil || bytes == 0 {
		return nil
	}
	if bytes < 0 {
		return fmt.Errorf("%w: negative %s byte count", b.limitError, b.description)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.limit-b.used {
		return fmt.Errorf("%w: %s limit %d bytes, already wrote %d bytes, next write %d bytes", b.limitError, b.description, b.limit, b.used, bytes)
	}
	b.used += bytes
	return nil
}

func (b *installBudget) canConsume(bytes uint64) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - b.used
	if remaining < 0 || bytes > uint64(remaining) {
		return fmt.Errorf("%w: %s limit %d bytes, already wrote %d bytes, entry declares %d bytes", b.limitError, b.description, b.limit, b.used, bytes)
	}
	return nil
}

func (b *installBudget) ensureAtLeast(bytes int64) error {
	if b == nil {
		return nil
	}
	if bytes < 0 {
		return fmt.Errorf("%w: invalid staged byte count", ErrInstallSizeLimit)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes > b.limit {
		return fmt.Errorf("%w: staged tree contains %d bytes, limit is %d bytes", ErrInstallSizeLimit, bytes, b.limit)
	}
	if bytes > b.used {
		b.used = bytes
	}
	return nil
}

type installBudgetContextKey struct{}
type downloadBudgetContextKey struct{}

func withInstallBudget(ctx context.Context, budget *installBudget) context.Context {
	return context.WithValue(ctx, installBudgetContextKey{}, budget)
}

func budgetFromContext(ctx context.Context) *installBudget {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(installBudgetContextKey{}).(*installBudget)
	return budget
}

func withDownloadBudget(ctx context.Context, budget *installBudget) context.Context {
	return context.WithValue(ctx, downloadBudgetContextKey{}, budget)
}

func downloadBudgetFromContext(ctx context.Context) *installBudget {
	if ctx == nil {
		return nil
	}
	budget, _ := ctx.Value(downloadBudgetContextKey{}).(*installBudget)
	return budget
}

type budgetWriter struct {
	destination io.Writer
	budget      *installBudget
}

type downloadBudgetWriter struct {
	destination io.Writer
	budget      *installBudget
}

func (w *downloadBudgetWriter) Write(buffer []byte) (int, error) {
	if err := w.budget.consume(int64(len(buffer))); err != nil {
		return 0, err
	}
	return w.destination.Write(buffer)
}

func (w *budgetWriter) Write(buffer []byte) (int, error) {
	if err := w.budget.consume(int64(len(buffer))); err != nil {
		return 0, err
	}
	return w.destination.Write(buffer)
}
