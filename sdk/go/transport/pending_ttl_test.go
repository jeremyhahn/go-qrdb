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

package transport

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCreateUserRequest_PendingTTLDuration(t *testing.T) {
	var nilReq *CreateUserRequest
	if d, err := nilReq.PendingTTLDuration(); err != nil || d != 0 {
		t.Fatalf("nil request = %v, %v; want 0, nil", d, err)
	}
	if d, err := (&CreateUserRequest{}).PendingTTLDuration(); err != nil || d != 0 {
		t.Fatalf("empty TTL = %v, %v; want 0, nil", d, err)
	}
	if d, err := (&CreateUserRequest{PendingTTL: "15m"}).PendingTTLDuration(); err != nil || d != 15*time.Minute {
		t.Fatalf("15m = %v, %v", d, err)
	}
	if _, err := (&CreateUserRequest{PendingTTL: "soon"}).PendingTTLDuration(); err == nil {
		t.Fatal("an invalid duration parsed")
	}
	for _, neg := range []string{"-500ms", "-1ns", "-1h"} {
		d, err := (&CreateUserRequest{PendingTTL: neg}).PendingTTLDuration()
		var nerr *NegativePendingTTLError
		if !errors.As(err, &nerr) || d != 0 {
			t.Fatalf("%s = %v, %v; want 0 and NegativePendingTTLError", neg, d, err)
		}
	}
}

func TestCreateUserRequest_PendingTTLSeconds(t *testing.T) {
	for ttl, want := range map[string]int64{"": 0, "1s": 1, "90s": 90, "1h": 3600, "2000ms": 2} {
		if got, err := (&CreateUserRequest{PendingTTL: ttl}).PendingTTLSeconds(); err != nil || got != want {
			t.Fatalf("%q = %d, %v; want %d, nil", ttl, got, err, want)
		}
	}
	for _, sub := range []string{"500ms", "1500ms", "1999ms", "1ns", "999999999ns", "1h0m0.5s"} {
		got, err := (&CreateUserRequest{PendingTTL: sub}).PendingTTLSeconds()
		var serr *FractionalPendingTTLError
		if !errors.As(err, &serr) || got != 0 {
			t.Fatalf("%s = %d, %v; want 0 and FractionalPendingTTLError", sub, got, err)
		}
	}
	var nerr *NegativePendingTTLError
	if _, err := (&CreateUserRequest{PendingTTL: "-500ms"}).PendingTTLSeconds(); !errors.As(err, &nerr) {
		t.Fatalf("-500ms = %v, want NegativePendingTTLError", err)
	}
}

func TestPendingFieldsJSON(t *testing.T) {
	b, err := json.Marshal(&CreateUserRequest{Pending: true, PendingTTL: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"pending_ttl":"1h"`) {
		t.Fatalf("create request JSON %s lacks pending_ttl", b)
	}
	var u User
	if err := json.Unmarshal([]byte(`{"pending_expires_at":"2026-11-14T10:00:00Z"}`), &u); err != nil {
		t.Fatal(err)
	}
	if u.PendingExpiresAt != "2026-11-14T10:00:00Z" {
		t.Fatalf("PendingExpiresAt = %q", u.PendingExpiresAt)
	}
}
