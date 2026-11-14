// Copyright (c) 2026 Jeremy Hahn
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

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

func TestUsersCreate_PendingTTL(t *testing.T) {
	var got *user.CreateUserRequest
	svc := &mockUserService{createFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
		got = req
		exp := time.Date(2026, 11, 14, 10, 15, 0, 0, time.UTC)
		return &user.User{ID: 7, Username: req.Username, TenantID: req.TenantID,
			Lifecycle: user.LifecyclePending, PendingExpiresAt: &exp,
			CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
	}}
	if _, err := executeUsersCmd(t, svc, nil, "create", "--username", "pam",
		"--email", "pam@example.com", "--pending", "--pending-ttl", "15m"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got == nil || !got.Pending || got.PendingTTL != 15*time.Minute {
		t.Fatalf("service saw %+v; want pending with a 15m TTL", got)
	}

	got = nil
	if _, err := executeUsersCmd(t, svc, nil, "create", "--username", "pat",
		"--email", "pat@example.com", "--pending"); err != nil {
		t.Fatalf("create pending without TTL: %v", err)
	}
	if got == nil || !got.Pending || got.PendingTTL != 0 {
		t.Fatalf("service saw %+v; want pending with no TTL", got)
	}
}

func TestUsersCreate_PendingTTLRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"ttl without pending", []string{"--pending-ttl", "15m"}, "requires --pending"},
		{"negative ttl", []string{"--pending", "--pending-ttl", "-1m"}, "must not be negative"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			svc := &mockUserService{createFn: func(context.Context, *user.CreateUserRequest) (*user.User, error) {
				called = true
				return nil, nil
			}}
			args := append([]string{"create", "--username", "pam", "--email", "pam@example.com"}, tc.args...)
			_, err := executeUsersCmd(t, svc, nil, args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want %q", err, tc.want)
			}
			if called {
				t.Fatal("the service was called for a refused create")
			}
		})
	}
}
