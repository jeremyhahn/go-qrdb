package server

import (
	"context"
	"sync"
)

// Start runs the worker in its own goroutine under a child of ctx and
// returns stop, which cancels that context and blocks until Run has
// returned, so no move step (a store close, copy or reopen, a journal
// write) is still running when stop returns. A caller must call stop
// before it closes the catalog, the tenant stores or the host: a step cut
// short by the cancel resumes from the journal at the next start. stop is
// idempotent and safe to call from several goroutines; every call returns
// only once Run has returned.
func (w *TenantMoveWorker) Start(ctx context.Context) (stop func()) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(runCtx)
	}()
	var once sync.Once
	return func() {
		once.Do(cancel)
		<-done
	}
}
