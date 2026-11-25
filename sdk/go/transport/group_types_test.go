package transport

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A uint64 user ID crosses the group JSON wire as a decimal string, so a
// JavaScript client (the TS SDK, the Wails desktop) never rounds an ID above
// 2^53-1 to a different user.
func TestGroupMemberUserIDWireIsDecimalString(t *testing.T) {
	const id = uint64(1<<63 + 7)
	const cell = `"userID":"9223372036854775815"`

	member, err := json.Marshal(GroupMember{UserID: id, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(member, []byte(cell)) {
		t.Fatalf("GroupMember = %s, want %s", member, cell)
	}
	var gotMember GroupMember
	if err := json.Unmarshal(member, &gotMember); err != nil || gotMember.UserID != id {
		t.Fatalf("GroupMember round trip = %d, %v; want %d", gotMember.UserID, err, id)
	}

	req, err := json.Marshal(GroupMemberRequest{UserID: id, GroupID: "g-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(req, []byte(cell)) {
		t.Fatalf("GroupMemberRequest = %s, want %s", req, cell)
	}
	var gotReq GroupMemberRequest
	if err := json.Unmarshal(req, &gotReq); err != nil || gotReq.UserID != id {
		t.Fatalf("GroupMemberRequest round trip = %d, %v; want %d", gotReq.UserID, err, id)
	}

	if err := json.Unmarshal([]byte(`{"userID":42,"groupID":"g-1"}`), &gotReq); err == nil {
		t.Fatal("a numeric userID decoded, want a refusal")
	}
}
