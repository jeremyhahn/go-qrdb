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

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// lockAnswer is a held lock as the server puts it on the wire.
var lockAnswer = lockWireResponse{Key: "k1", FencingToken: 3, LeaseID: 9, HolderID: "h1", Reentrant: true,
	AcquireCount: 2, TTL: 60, AcquiredAt: 1700000000}

// checkLock reports how got differs from lockAnswer.
func checkLock(got *transport.LockInfo) error {
	want := transport.LockInfo{Key: "k1", FencingToken: 3, LeaseID: 9, HolderID: "h1", Reentrant: true,
		AcquireCount: 2, TTL: 60, AcquiredAt: 1700000000}
	if got == nil || *got != want {
		return fmt.Errorf("lock decoded as %+v, want %+v", got, want)
	}
	return nil
}

// TestRESTLock_SendsTheDocumentedRequest checks each lock RPC's verb, path,
// query and body, and that the answer decodes into the result.
func TestRESTLock_SendsTheDocumentedRequest(t *testing.T) {
	acquire := &transport.LockAcquireRequest{Key: "k1", HolderID: "h1", TTL: 60, Reentrant: true}
	acquireBody := lockAcquireWireRequest{Key: "k1", HolderID: "h1", TTL: 60, Reentrant: true}

	assertSendsTheDocumentedRequest(t, []wireCase{
		{name: "LockAcquire", verb: http.MethodPost, path: "/lock/acquire", wantBody: acquireBody, answer: lockAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.LockAcquire(ctx, acquire)
				if err != nil {
					return err
				}
				return checkLock(info)
			}},
		{name: "LockTryLock", verb: http.MethodPost, path: "/lock/trylock", wantBody: acquireBody, answer: lockAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.LockTryLock(ctx, acquire)
				if err != nil {
					return err
				}
				return checkLock(info)
			}},
		{name: "LockRelease", verb: http.MethodPost, path: "/lock/release",
			wantBody: lockReleaseWireRequest{Key: "k1", FencingToken: 3}, answer: struct{}{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				return rt.LockRelease(ctx, &transport.LockReleaseRequest{Key: "k1", FencingToken: 3})
			}},
		{name: "LockExtend", verb: http.MethodPost, path: "/lock/extend",
			wantBody: lockExtendWireRequest{Key: "k1", FencingToken: 3}, answer: lockAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.LockExtend(ctx, &transport.LockExtendRequest{Key: "k1", FencingToken: 3})
				if err != nil {
					return err
				}
				return checkLock(info)
			}},
		{name: "LockStatus", verb: http.MethodGet, path: "/lock/status", rawQuery: "key=a%2Fb", answer: lockAnswer,
			callCheck: func(ctx context.Context, rt *Transport) error {
				info, err := rt.LockStatus(ctx, "a/b")
				if err != nil {
					return err
				}
				return checkLock(info)
			}},
		{name: "LockList/Prefix", verb: http.MethodGet, path: "/lock/list", rawQuery: "prefix=a%2F",
			answer: locksWireResponse{Locks: []*lockWireResponse{&lockAnswer}},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.LockList(ctx, "a/")
				if err != nil {
					return err
				}
				if len(resp.Locks) != 1 {
					return fmt.Errorf("got %d locks, want 1", len(resp.Locks))
				}
				return checkLock(resp.Locks[0])
			}},
		{name: "LockList/NoPrefix", verb: http.MethodGet, path: "/lock/list", answer: locksWireResponse{},
			callCheck: func(ctx context.Context, rt *Transport) error {
				resp, err := rt.LockList(ctx, "")
				if err == nil && len(resp.Locks) != 0 {
					return fmt.Errorf("got %d locks, want none", len(resp.Locks))
				}
				return err
			}},
	})
}

// TestRESTLock_SurfacesAFailedAnswer checks that each lock RPC reports a
// failed answer as a RequestError naming it.
func TestRESTLock_SurfacesAFailedAnswer(t *testing.T) {
	assertSurfacesAFailedAnswer(t, []failedAnswerCase{
		{"LockAcquire", http.MethodPost, "/lock/acquire", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockAcquire(ctx, &transport.LockAcquireRequest{Key: "k1"})
			return err
		}},
		{"LockTryLock", http.MethodPost, "/lock/trylock", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockTryLock(ctx, &transport.LockAcquireRequest{Key: "k1"})
			return err
		}},
		{"LockRelease", http.MethodPost, "/lock/release", false, func(ctx context.Context, rt *Transport) error {
			return rt.LockRelease(ctx, &transport.LockReleaseRequest{Key: "k1"})
		}},
		{"LockExtend", http.MethodPost, "/lock/extend", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockExtend(ctx, &transport.LockExtendRequest{Key: "k1"})
			return err
		}},
		{"LockStatus", http.MethodGet, "/lock/status", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockStatus(ctx, "k1")
			return err
		}},
		{"LockList", http.MethodGet, "/lock/list", true, func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockList(ctx, "k")
			return err
		}},
	})
}

// TestRESTLock_Status_NotFound checks that the server's LOCK_NOT_FOUND
// refusal reads as "not locked": no lock and no error.
func TestRESTLock_Status_NotFound(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/lock/status"), func(w http.ResponseWriter, _ *http.Request) {
		notFound := &lock.LockNotFoundError{Key: "k1"}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{
			Error: notFound.Error(), Code: lock.ReasonLockNotFound, Metadata: notFound.Metadata(),
		})
	})
	info, err := rt.LockStatus(context.Background(), "k1")
	if err != nil || info != nil {
		t.Fatalf("LockStatus = %+v, %v; want nil, nil", info, err)
	}
}

// TestRESTLock_Status_NotFoundWithoutLockReasonIsAnError checks that a 404
// without the LOCK_NOT_FOUND reason -- a missing route or database -- is a
// failed read, not "not locked".
func TestRESTLock_Status_NotFoundWithoutLockReasonIsAnError(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/lock/status"), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "404 page not found", http.StatusNotFound)
	})
	info, err := rt.LockStatus(context.Background(), "k1")
	if reqErr, ok := errors.AsType[*transport.RequestError](err); !ok || reqErr.StatusCode != http.StatusNotFound {
		t.Fatalf("LockStatus = %+v, %v; want a RequestError carrying 404", info, err)
	}
}

// TestRESTLock_Closed checks that every lock RPC on a closed transport
// refuses before the wire.
func TestRESTLock_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(ctx context.Context, rt *Transport) error{
		"LockAcquire": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockAcquire(ctx, &transport.LockAcquireRequest{})
			return err
		},
		"LockTryLock": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockTryLock(ctx, &transport.LockAcquireRequest{})
			return err
		},
		"LockRelease": func(ctx context.Context, rt *Transport) error {
			return rt.LockRelease(ctx, &transport.LockReleaseRequest{})
		},
		"LockExtend": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockExtend(ctx, &transport.LockExtendRequest{})
			return err
		},
		"LockStatus": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockStatus(ctx, "k1")
			return err
		},
		"LockList": func(ctx context.Context, rt *Transport) error {
			_, err := rt.LockList(ctx, "")
			return err
		},
	})
}
