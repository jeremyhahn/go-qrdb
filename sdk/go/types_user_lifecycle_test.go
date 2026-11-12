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

package sdk_test

import (
	"bytes"
	"encoding/json"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// This file is deliberately in the EXTERNAL test package and imports ONLY
// sdk/go. That is what makes it a consumer test: it exercises the identity
// surface exactly as go-xkms and go-quicbaas must reach it, so a field or
// constant that exists in pkg/user but was never re-exported fails to compile
// here rather than being discovered by a consumer at bump time.

// The remote transport DTO must carry every identity field too, or a consumer
// on REST/gRPC/QUIC/MCP/unix reads a user that is missing exactly the fields
// this change exists to give it.
func TestSDK_TransportUserCarriesEveryIdentityField(t *testing.T) {
	u := transport.User{
		ID:          42,
		Username:    "operator",
		Email:       "operator@example.com",
		TenantID:    "tenant-1",
		Lifecycle:   string(sdk.LifecycleActive),
		AuthMethods: []string{"webauthn"},
		LastAuth:    "2026-09-01T12:00:00Z",
	}

	// It also has to survive a JSON round trip on the wire, which is where an
	// `omitempty` on a "clear" field would quietly destroy the semantics.
	data, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back transport.User
	if uerr := json.Unmarshal(data, &back); uerr != nil {
		t.Fatalf("unmarshal: %v", uerr)
	}
	if back.Lifecycle != u.Lifecycle ||
		len(back.AuthMethods) != 1 || back.LastAuth != u.LastAuth {
		t.Fatalf("round trip lost fields: %+v", back)
	}
	// Lifecycle is the only account state and group membership is not a qrdb
	// field: neither removed key may reappear on the wire.
	var raw map[string]json.RawMessage
	if rerr := json.Unmarshal(data, &raw); rerr != nil {
		t.Fatalf("unmarshal raw: %v", rerr)
	}
	for _, removed := range []string{"enabled", "group_ids"} {
		if _, ok := raw[removed]; ok {
			t.Errorf("transport.User carries the removed %q key: %s", removed, data)
		}
	}
}

// An update request must encode the three-way distinction: nil leaves a set
// alone, an empty non-nil slice clears it. `omitempty` would drop the empty
// slice from the body and make a clear silently do nothing, so its absence on
// this field is load-bearing and pinned here.
func TestSDK_TransportUpdateRequestEncodesClearVersusLeaveAlone(t *testing.T) {
	clear, err := json.Marshal(transport.UpdateUserRequest{ID: 1, AuthMethods: []string{}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(clear, []byte(`"auth_methods":[]`)) {
		t.Fatalf("a cleared AuthMethods must encode as []; got %s", clear)
	}

	leave, err := json.Marshal(transport.UpdateUserRequest{ID: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(leave, []byte(`"auth_methods":null`)) {
		t.Fatalf("an untouched AuthMethods must encode as null; got %s", leave)
	}
}
