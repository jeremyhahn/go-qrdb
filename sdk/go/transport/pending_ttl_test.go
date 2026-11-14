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
