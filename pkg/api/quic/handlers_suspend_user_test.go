package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// suspensionHandlers are the two QUIC suspension verbs, driven through one
// table because their wire shape and error paths are the same.
var suspensionHandlers = []struct {
	name    string
	mock    func(fn func(context.Context, uint64) error) *mockUserService
	handler func(*Server) func(context.Context, []byte) ([]byte, error)
	payload func(id uint64) []byte
}{
	{
		name: "user.Suspend",
		mock: func(fn func(context.Context, uint64) error) *mockUserService {
			return &mockUserService{suspendUserFn: fn}
		},
		handler: func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleSuspendUser },
		payload: func(id uint64) []byte { b, _ := json.Marshal(suspendUserWireRequest{ID: id}); return b },
	},
	{
		name: "user.Unsuspend",
		mock: func(fn func(context.Context, uint64) error) *mockUserService {
			return &mockUserService{unsuspendUserFn: fn}
		},
		handler: func(s *Server) func(context.Context, []byte) ([]byte, error) { return s.handleUnsuspendUser },
		payload: func(id uint64) []byte { b, _ := json.Marshal(unsuspendUserWireRequest{ID: id}); return b },
	},
}

func TestHandleSuspension_Success(t *testing.T) {
	for _, tc := range suspensionHandlers {
		t.Run(tc.name, func(t *testing.T) {
			var got uint64
			srv := newUserServer(tc.mock(func(_ context.Context, id uint64) error { got = id; return nil }))
			resp, err := tc.handler(srv)(context.Background(), tc.payload(1005))
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			var out statusWireResponse
			if err := json.Unmarshal(resp, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if out.Status != "ok" || got != 1005 {
				t.Fatalf("status %q, id %d; want ok, 1005", out.Status, got)
			}
		})
	}
}

func TestHandleSuspension_ServiceError(t *testing.T) {
	for _, tc := range suspensionHandlers {
		t.Run(tc.name, func(t *testing.T) {
			srv := newUserServer(tc.mock(func(context.Context, uint64) error { return errMockUser }))
			if _, err := tc.handler(srv)(context.Background(), tc.payload(1006)); !errors.Is(err, errMockUser) {
				t.Fatalf("expected errMockUser, got %v", err)
			}
		})
	}
}

func TestHandleSuspension_NilService(t *testing.T) {
	for _, tc := range suspensionHandlers {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(nil, nil, "localhost:0")
			_, err := tc.handler(srv)(context.Background(), tc.payload(1))
			var snc *ServiceNotConfiguredError
			if !errors.As(err, &snc) {
				t.Fatalf("expected ServiceNotConfiguredError, got %T: %v", err, err)
			}
		})
	}
}

func TestHandleSuspension_InvalidJSON(t *testing.T) {
	for _, tc := range suspensionHandlers {
		t.Run(tc.name, func(t *testing.T) {
			srv := newUserServer(tc.mock(nil))
			_, err := tc.handler(srv)(context.Background(), []byte("{bad"))
			var ue *UnmarshalError
			if !errors.As(err, &ue) {
				t.Fatalf("expected UnmarshalError, got %T: %v", err, err)
			}
		})
	}
}
