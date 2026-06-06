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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// TestHandlerContextCanceled_SurfacesErrorNotHang asserts that when the
// backend returns context.Canceled (simulating a stalled Raft SyncRead that
// was cut off by the handler's WithTimeout), the handler writes a 500 JSON
// error payload immediately instead of hanging or returning an empty body.
//
// This is a regression guard for the fix that wraps every backend call in
// context.WithTimeout(r.Context(), apiRequestTimeout) / defer cancel().
func TestHandlerContextCanceled_SurfacesErrorNotHang(t *testing.T) {
	t.Parallel()

	canceledErr := context.Canceled

	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		server  func() *Server
		wantMin int // minimum acceptable HTTP status (must be >= 500)
	}{
		{
			name:   "handleCreateRole_context_canceled",
			method: http.MethodPost,
			path:   "/roles",
			body:   `{"name":"x","tenantID":"t1","permissions":[]}`,
			server: func() *Server {
				svc := &mockRoleService{
					createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
						return nil, canceledErr
					},
				}
				return NewServer(nil, nil, "127.0.0.1:0", WithRoleService(svc))
			},
			wantMin: http.StatusInternalServerError,
		},
		{
			name:   "handleListRoles_context_canceled",
			method: http.MethodGet,
			path:   "/roles/list",
			server: func() *Server {
				svc := &mockRoleService{
					listRolesFn: func(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
						return nil, canceledErr
					},
				}
				return NewServer(nil, nil, "127.0.0.1:0", WithRoleService(svc))
			},
			wantMin: http.StatusInternalServerError,
		},
		{
			name:   "handleCreateUser_context_canceled",
			method: http.MethodPost,
			path:   "/users",
			body:   `{"username":"alice","tenantID":"t1"}`,
			server: func() *Server {
				svc := &mockUserService{
					createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
						return nil, canceledErr
					},
				}
				return NewServer(nil, nil, "127.0.0.1:0", WithUserService(svc))
			},
			wantMin: http.StatusInternalServerError,
		},
		{
			name:   "handleKVGet_context_canceled",
			method: http.MethodGet,
			path:   "/kv/mykey",
			server: func() *Server {
				svc := &mockKVService{
					getFn: func(_ context.Context, _ string) ([]byte, error) {
						return nil, canceledErr
					},
				}
				return NewServer(svc, &mockAdminService{}, "127.0.0.1:0")
			},
			wantMin: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := tc.server()

			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			} else {
				bodyReader = strings.NewReader("")
			}

			req := httptest.NewRequest(tc.method, tc.path, bodyReader)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			srv.dispatch(rec, req)

			if rec.Code < tc.wantMin {
				t.Errorf("%s: got HTTP %d, want >= %d", tc.name, rec.Code, tc.wantMin)
			}

			// Response must not be empty — a clean error payload must be written.
			body := rec.Body.String()
			if strings.TrimSpace(body) == "" {
				t.Errorf("%s: response body is empty, expected JSON error payload", tc.name)
			}

			// Body must be valid JSON (not a partial or truncated write).
			var payload map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &payload); err != nil {
				t.Errorf("%s: response body is not valid JSON: %v\nbody: %s", tc.name, err, body)
			}
		})
	}
}
