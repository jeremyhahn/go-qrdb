package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHandler_StatusVersion asserts GET /status reports the build version
// set with WithVersion, and an empty version when none is set.
func TestHandler_StatusVersion(t *testing.T) {
	adminSvc := &mockAdminService{
		statusFn: func(_ context.Context) (*service.StatusInfo, error) {
			return &service.StatusInfo{NodeID: "node-1"}, nil
		},
	}
	cases := []struct {
		name string
		opts []ServerOption
		want string
	}{
		{"with version", []ServerOption{WithVersion("1.2.3-test")}, "1.2.3-test"},
		{"without version", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(&mockKVService{}, adminSvc, "127.0.0.1:0", tc.opts...)
			rec := httptest.NewRecorder()
			srv.dispatch(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /status status = %d, want %d", rec.Code, http.StatusOK)
			}
			var raw map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
				t.Fatalf("decode status response: %v", err)
			}
			got, ok := raw["version"].(string)
			if !ok {
				t.Fatalf("status response has no string version field: %v", raw)
			}
			if got != tc.want {
				t.Fatalf("status version = %q, want %q", got, tc.want)
			}
		})
	}
}
