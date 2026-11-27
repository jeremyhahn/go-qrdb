// Package pinhook lets a test see the SO PIN copies transport.ResolveSOPIN
// hands out, so it can check that the transport wiped each one once the call
// returned. The recorder travels in the call's context: a call made without
// one records nothing, and parallel tests never see each other's copies.
package pinhook

import (
	"context"
	"sync"
)

type recorderKey struct{}

// Recorder keeps an alias to every SO PIN copy resolved under its context.
type Recorder struct {
	mu     sync.Mutex
	copies [][]byte
}

// With returns ctx carrying r.
func With(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, r)
}

// Record keeps an alias to pin when ctx carries a Recorder.
func Record(ctx context.Context, pin []byte) {
	r, ok := ctx.Value(recorderKey{}).(*Recorder)
	if !ok || r == nil {
		return
	}
	r.mu.Lock()
	r.copies = append(r.copies, pin)
	r.mu.Unlock()
}

// Copies returns the aliases recorded so far.
func (r *Recorder) Copies() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]byte(nil), r.copies...)
}
