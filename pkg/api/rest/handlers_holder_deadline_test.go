// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// holderCallDeadline runs one holder helper under a request carrying
// callerCtx and returns the deadline the operation observed and its status.
func holderCallDeadline(t *testing.T, callerCtx context.Context, run func(w http.ResponseWriter, r *http.Request, observe func(ctx context.Context))) (time.Time, bool, int) {
	t.Helper()
	var (
		deadline time.Time
		has      bool
	)
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(callerCtx)
	rec := httptest.NewRecorder()
	run(rec, req, func(ctx context.Context) {
		deadline, has = ctx.Deadline()
	})
	return deadline, has, rec.Code
}

// holderHelpers are the REST database and tenant holder call paths, each
// adapted to report the context its operation runs under.
func holderHelpers(t *testing.T) map[string]func(w http.ResponseWriter, r *http.Request, observe func(ctx context.Context)) {
	t.Helper()
	dbSrv := newDBHolderServer(t)
	tenantSrv := newTenantServer(&mockTenantService{})
	return map[string]func(w http.ResponseWriter, r *http.Request, observe func(ctx context.Context)){
		"database": func(w http.ResponseWriter, r *http.Request, observe func(ctx context.Context)) {
			dbSrv.databaseHolderCall(w, r, nil, func(ctx context.Context, _ service.DatabaseHolderServicer) (any, error) {
				observe(ctx)
				return map[string]bool{"ok": true}, nil
			})
		},
		"tenant": func(w http.ResponseWriter, r *http.Request, observe func(ctx context.Context)) {
			tenantSrv.tenantHolderCall(w, r, "acme", permission.Permission("barrier:officer"), nil, func(ctx context.Context) (any, error) {
				observe(ctx)
				return map[string]bool{"ok": true}, nil
			})
		},
	}
}

// TestHolderCalls_HonourCallerDeadline pins that a holder operation runs
// under the caller's deadline, not a server-imposed cap: a caller allowing
// an hour gets an hour, as system-holder REST and every other transport do.
func TestHolderCalls_HonourCallerDeadline(t *testing.T) {
	for name, run := range holderHelpers(t) {
		t.Run(name, func(t *testing.T) {
			want := time.Now().Add(time.Hour)
			ctx, cancel := context.WithDeadline(context.Background(), want)
			defer cancel()
			got, has, code := holderCallDeadline(t, ctx, run)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if !has || !got.Equal(want) {
				t.Fatalf("operation deadline = %v (set %v), want the caller's %v", got, has, want)
			}
		})
	}
}

// TestHolderCalls_NoCallerDeadlineAddsNone pins that a caller without a
// deadline is not given one: the route adds no timeout of its own.
func TestHolderCalls_NoCallerDeadlineAddsNone(t *testing.T) {
	for name, run := range holderHelpers(t) {
		t.Run(name, func(t *testing.T) {
			got, has, code := holderCallDeadline(t, context.Background(), run)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if has {
				t.Fatalf("operation deadline = %v, want none", got)
			}
		})
	}
}
