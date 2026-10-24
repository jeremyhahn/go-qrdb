// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package embedded

import "context"

// ContextBinder binds the caller's identity into the context of one embedded
// call. It returns the context the auth middleware and the handler see, or an
// error that fails the call before anything dispatches.
type ContextBinder func(ctx context.Context) (context.Context, error)

// WithContextBinder installs a binder that runs once before every unary and
// streaming dispatch, ahead of the auth middleware (WithAuthMiddleware). It
// is the in-process counterpart of a network transport's authenticating
// listener: a composer whose caller holds no identity on its context cannot
// reach a handler through it, gated or not. The binder's error is returned
// unchanged. A nil binder makes New fail with *NilContextBinderError. With
// no binder installed the context is passed through untouched.
func WithContextBinder(b ContextBinder) Option {
	return func(t *Transport) {
		t.binder = &contextBinder{bind: b}
	}
}

// contextBinder holds the installed ContextBinder. Its address marks a
// context it has bound, so a call one entry point makes through another
// (Request through Put, a database-scoped clone through the shared binder)
// binds once.
type contextBinder struct {
	bind ContextBinder
}

// boundKey is the context key under which bind records the contextBinder
// that bound the context.
type boundKey struct{}

// NilContextBinderError is returned by New when WithContextBinder was given
// a nil binder.
type NilContextBinderError struct{}

// Error returns a human-readable description.
func (e *NilContextBinderError) Error() string {
	return "embedded: context binder must not be nil"
}

// NilBoundContextError refuses a call whose ContextBinder returned a nil
// context and no error.
type NilBoundContextError struct {
	// Method is the refused operation.
	Method string
}

// Error names the refused operation.
func (e *NilBoundContextError) Error() string {
	return "embedded: " + e.Method + ": context binder returned a nil context"
}

// admit is the one gate every dispatch entry point passes: the sealed-window
// and awaiting-admin-credential gate, then the context binder. The returned
// context is the one the entry point hands to the auth middleware and the
// handler.
func (t *Transport) admit(ctx context.Context, method string) (context.Context, error) {
	if err := t.gate(method); err != nil {
		return ctx, err
	}
	return t.bind(ctx, method)
}

// bind runs the installed ContextBinder on ctx, unless this binder already
// bound it.
func (t *Transport) bind(ctx context.Context, method string) (context.Context, error) {
	b := t.binder
	if b == nil {
		return ctx, nil
	}
	if bound, _ := ctx.Value(boundKey{}).(*contextBinder); bound == b {
		return ctx, nil
	}
	bctx, err := b.bind(ctx)
	if err != nil {
		return ctx, err
	}
	if bctx == nil {
		return ctx, &NilBoundContextError{Method: method}
	}
	return context.WithValue(bctx, boundKey{}, b), nil
}
