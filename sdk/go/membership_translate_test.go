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

package sdk

import (
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTranslateMemberEvent lives in package sdk because translateMemberEvent
// is unexported. It used to reach it from package sdk_test through an
// export_test.go alias, which gosec -tests cannot typecheck (#408).
func TestTranslateMemberEvent(t *testing.T) {
	m := cluster.Member{NodeID: 2, Address: "10.0.0.2:5000"}
	data, _ := json.Marshal(m)
	key := []byte(cluster.MembershipPrefix + "2")

	// PUT -> joined.
	ev, ok := translateMemberEvent(&transport.WatchEvent{
		Type: transport.EventPut,
		Kv:   &transport.MVCCKeyValue{Key: key, Value: data},
	})
	if !ok || ev.Type != cluster.MemberJoined || ev.Member.NodeID != 2 {
		t.Fatalf("PUT translate = %+v ok=%v, want joined node 2", ev, ok)
	}

	// DELETE with PrevKv -> left with full member.
	ev, ok = translateMemberEvent(&transport.WatchEvent{
		Type:   transport.EventDelete,
		PrevKv: &transport.MVCCKeyValue{Key: key, Value: data},
	})
	if !ok || ev.Type != cluster.MemberLeft || ev.Member.NodeID != 2 {
		t.Fatalf("DELETE(prevkv) translate = %+v ok=%v, want left node 2", ev, ok)
	}

	// DELETE without PrevKv -> left, node derived from key.
	ev, ok = translateMemberEvent(&transport.WatchEvent{
		Type: transport.EventDelete,
		Kv:   &transport.MVCCKeyValue{Key: key},
	})
	if !ok || ev.Type != cluster.MemberLeft || ev.Member.NodeID != 2 {
		t.Fatalf("DELETE(no prevkv) translate = %+v ok=%v, want left node 2", ev, ok)
	}

	// PUT with nil Kv -> skipped.
	if _, ok := translateMemberEvent(&transport.WatchEvent{Type: transport.EventPut}); ok {
		t.Fatal("PUT with nil Kv should be skipped")
	}
	// Unknown type -> skipped.
	if _, ok := translateMemberEvent(&transport.WatchEvent{Type: transport.EventType(42)}); ok {
		t.Fatal("unknown event type should be skipped")
	}
}
