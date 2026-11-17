package cluster

import (
	"context"
	"sync"
	"testing"
	"time"
)

type hookCall struct {
	nodeID uint64
	joined bool
}

// TestMembershipManager_MemberChangeHook checks the hook fires once for a
// new member's join and once for its leave, and never for the local node
// or a re-registration of a member already known.
func TestMembershipManager_MemberChangeHook(t *testing.T) {
	store := newFakeStore()
	var mu sync.Mutex
	var calls []hookCall
	left := make(chan struct{}, 1)
	hook := func(_ context.Context, mem Member, joined bool) {
		mu.Lock()
		calls = append(calls, hookCall{nodeID: mem.NodeID, joined: joined})
		mu.Unlock()
		if !joined {
			left <- struct{}{}
		}
	}
	m, cancel := startManager(t, store, WithMemberChangeHook(hook))
	defer cancel()

	self := selfMember()
	peer := Member{NodeID: 2, Address: "10.0.0.2:5000"}
	store.events <- MemberEvent{Type: MemberJoined, Member: self}
	store.events <- MemberEvent{Type: MemberJoined, Member: peer}
	store.events <- MemberEvent{Type: MemberJoined, Member: peer} // re-registration
	store.events <- MemberEvent{Type: MemberLeft, Member: peer}

	select {
	case <-left:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the leave hook")
	}
	// Close waits for every hook goroutine, so the record is final.
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var joins, leaves int
	for _, c := range calls {
		if c.nodeID != peer.NodeID {
			t.Fatalf("hook fired for node %d, want only %d", c.nodeID, peer.NodeID)
		}
		if c.joined {
			joins++
		} else {
			leaves++
		}
	}
	if joins != 1 || leaves != 1 {
		t.Fatalf("hook calls = %d joins, %d leaves, want 1 and 1", joins, leaves)
	}
}
