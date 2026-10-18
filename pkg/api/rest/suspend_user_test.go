package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

func TestHandleSuspendUser_Success(t *testing.T) {
	var got uint64
	svc := &mockUserService{
		suspendUserFn: func(_ context.Context, id uint64) error { got = id; return nil },
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/1001/suspend", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp okResponse
	decodeJSON(t, rec, &resp)
	if resp.Status != "ok" || got != 1001 {
		t.Fatalf("status %q, id %d; want ok, 1001", resp.Status, got)
	}
}

func TestHandleSuspendUser_TransitionRefusedIsConflict(t *testing.T) {
	svc := &mockUserService{
		suspendUserFn: func(_ context.Context, id uint64) error {
			return &user.LifecycleTransitionError{ID: id, From: user.LifecycleDisabled, To: user.LifecycleSuspended}
		},
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/9001/suspend", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusConflict)
}

func TestHandleUnsuspendUser_Success(t *testing.T) {
	var got uint64
	svc := &mockUserService{
		unsuspendUserFn: func(_ context.Context, id uint64) error { got = id; return nil },
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/1002/unsuspend", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	if got != 1002 {
		t.Fatalf("id = %d, want 1002", got)
	}
}

func TestHandleUnsuspendUser_NotFound(t *testing.T) {
	svc := &mockUserService{
		unsuspendUserFn: func(_ context.Context, id uint64) error { return &user.UserNotFoundError{ID: id} },
	}
	srv := newAuthServer(svc, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/users/9002/unsuspend", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusNotFound)
}

func TestHandleSuspension_BadID(t *testing.T) {
	srv := newAuthServer(&mockUserService{}, nil, nil)
	for _, path := range []string{"/users/not-a-number/suspend", "/users/not-a-number/unsuspend"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		srv.dispatch(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", path, rec.Code)
		}
	}
}
