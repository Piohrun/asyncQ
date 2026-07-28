package plugin

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDatasourceDisposed is the terminal cancellation cause for a datasource
// instance. Callers can use errors.Is to distinguish disposal from ordinary
// request cancellation.
var ErrDatasourceDisposed = errors.New("kdb datasource disposed")

type datasourceLifecycleContextKey struct{}

type datasourceLifecycleLease struct {
	datasource *KdbDatasource
	active     atomic.Bool
}

type datasourceMergedContext struct {
	context.Context
	values context.Context
}

func (c *datasourceMergedContext) Value(key any) any {
	if value := c.Context.Value(key); value != nil {
		return value
	}
	return c.values.Value(key)
}

func (d *KdbDatasource) initializeLifecycle() {
	d.lifecycleMu.Lock()
	d.initializeLifecycleLocked()
	d.lifecycleMu.Unlock()
}

func (d *KdbDatasource) initializeLifecycleLocked() {
	if d.lifecycleCtx != nil {
		return
	}
	d.lifecycleCtx, d.lifecycleCancel = context.WithCancelCause(context.Background())
}

func (d *KdbDatasource) beginOperation(ctx context.Context) (context.Context, func(), error) {
	ctx = normalizeSyncQueryContext(ctx)

	d.lifecycleMu.Lock()
	d.initializeLifecycleLocked()
	if d.lifecycleStopping {
		d.lifecycleMu.Unlock()
		return nil, func() {}, ErrDatasourceDisposed
	}
	if err := syncQueryContextError(ctx, "datasource operation canceled before admission"); err != nil {
		d.lifecycleMu.Unlock()
		return nil, func() {}, err
	}

	lease := &datasourceLifecycleLease{datasource: d}
	lease.active.Store(true)
	operationCtx, cleanupContext := mergeDatasourceContext(ctx, d.lifecycleCtx)
	operationCtx = context.WithValue(operationCtx, datasourceLifecycleContextKey{}, lease)
	d.lifecycleWG.Add(1)
	d.lifecycleMu.Unlock()

	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() {
			lease.active.Store(false)
			cleanupContext()
			d.lifecycleWG.Done()
		})
	}
	return operationCtx, finish, nil
}

func (d *KdbDatasource) ensureOperationContext(ctx context.Context) (context.Context, func(), error) {
	ctx = normalizeSyncQueryContext(ctx)
	if d.hasActiveLifecycleLease(ctx) {
		return ctx, func() {}, nil
	}
	return d.beginOperation(ctx)
}

func (d *KdbDatasource) hasActiveLifecycleLease(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	lease, _ := ctx.Value(datasourceLifecycleContextKey{}).(*datasourceLifecycleLease)
	return lease != nil && lease.datasource == d && lease.active.Load()
}

func mergeDatasourceContext(parent context.Context, datasourceCtx context.Context) (context.Context, func()) {
	parent = normalizeSyncQueryContext(parent)
	merged, cancel := context.WithCancelCause(datasourceCtx)
	effective := context.Context(merged)
	cancelDeadline := func() {}
	if deadline, ok := parent.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		effective, deadlineCancel = context.WithDeadline(effective, deadline)
		cancelDeadline = deadlineCancel
	}
	effective = &datasourceMergedContext{Context: effective, values: parent}

	callbackDone := make(chan struct{})
	stop := func() bool { return true }
	if parent.Done() != nil {
		stop = context.AfterFunc(parent, func() {
			cause := context.Cause(parent)
			if cause == nil {
				cause = parent.Err()
			}
			cancel(cause)
			close(callbackDone)
		})
	}
	cleanup := func() {
		if !stop() {
			<-callbackDone
		}
		cancelDeadline()
		cancel(nil)
	}
	return effective, cleanup
}

func (d *KdbDatasource) startDetachedTask(ctx context.Context, timeout time.Duration, task func(context.Context)) bool {
	if task == nil {
		return false
	}
	base := context.WithoutCancel(normalizeSyncQueryContext(ctx))

	d.lifecycleMu.Lock()
	d.initializeLifecycleLocked()
	if d.lifecycleStopping {
		d.lifecycleMu.Unlock()
		return false
	}

	lease := &datasourceLifecycleLease{datasource: d}
	lease.active.Store(true)
	taskCtx, cleanupContext := mergeDatasourceContext(base, d.lifecycleCtx)
	taskCtx = context.WithValue(taskCtx, datasourceLifecycleContextKey{}, lease)
	var cancelTimeout context.CancelFunc = func() {}
	if timeout > 0 {
		taskCtx, cancelTimeout = context.WithTimeout(taskCtx, timeout)
	}
	d.lifecycleWG.Add(1)
	go func() {
		defer func() {
			lease.active.Store(false)
			cancelTimeout()
			cleanupContext()
			d.lifecycleWG.Done()
		}()
		task(taskCtx)
	}()
	d.lifecycleMu.Unlock()
	return true
}

func (d *KdbDatasource) withActiveLifecycle(ctx context.Context, action func() error) (err error) {
	if action == nil {
		return nil
	}

	// Commits take an independent admission even when the caller already owns
	// a lease. This keeps an accidentally unjoined commit registered after its
	// parent handler releases its own lease.
	ctx, finish, err := d.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer func() {
		err = disposedOperationError(ctx, err)
		finish()
	}()

	if err = syncQueryContextError(ctx, "datasource operation interrupted before commit"); err != nil {
		return err
	}
	err = action()
	return err
}

func disposedOperationError(ctx context.Context, operationErr error) error {
	if ctx == nil || !errors.Is(context.Cause(ctx), ErrDatasourceDisposed) {
		return operationErr
	}
	disposedErr := fmt.Errorf("datasource operation interrupted: %w", errors.Join(context.Canceled, ErrDatasourceDisposed))
	if operationErr == nil {
		return disposedErr
	}
	if errors.Is(operationErr, ErrDatasourceDisposed) {
		return operationErr
	}
	return errors.Join(operationErr, disposedErr)
}

func (d *KdbDatasource) Dispose() {
	logDatasourceDispose()

	d.lifecycleMu.Lock()
	d.initializeLifecycleLocked()
	if d.lifecycleStopping {
		done := d.lifecycleDone
		d.lifecycleMu.Unlock()
		<-done
		return
	}
	d.lifecycleStopping = true
	d.lifecycleDone = make(chan struct{})
	done := d.lifecycleDone
	d.lifecycleCancel(ErrDatasourceDisposed)
	d.lifecycleMu.Unlock()
	defer close(done)

	// Root cancellation interrupts dials and IPC first. Closing the pool then
	// forcibly releases active and idle pooled transports.
	d.closeSyncPool()
	d.lifecycleWG.Wait()
	d.closeSyncQueryCache()
	d.clearExcelReportDownloads()
}
