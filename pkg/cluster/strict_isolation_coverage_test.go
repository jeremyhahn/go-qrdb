package cluster

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// TestClusterErrors_NameTheirInput: every typed cluster error names the
// input it refuses, so an operator can act on the message alone.
func TestClusterErrors_NameTheirInput(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&PeerParseError{Input: "bad-peer@"}, "bad-peer@"},
		{&UnknownDiscoveryError{Name: "carrier-pigeon"}, "carrier-pigeon"},
		{&MissingFlagError{Flag: "--node-id"}, "--node-id"},
		{&NoPeersError{}, ""},
		{&EmptyAPIEndpointError{Protocol: APIProtocol("grpc")}, "grpc"},
		{&MembershipError{Reason: "roster unavailable"}, "roster unavailable"},
		{&MembershipCauseError{Reason: "register", Cause: errors.New("raft busy")}, "raft busy"},
	}
	for _, tc := range cases {
		msg := tc.err.Error()
		if msg == "" || !strings.Contains(msg, tc.want) {
			t.Errorf("%T.Error() = %q, want a message naming %q", tc.err, msg, tc.want)
		}
	}
}

// TestFireChange_ClosedManagerFiresNothing: once the manager is closed, or
// before it has a run context, neither change path starts the hook.
func TestFireChange_ClosedManagerFiresNothing(t *testing.T) {
	var calls atomic.Int32
	hook := func(context.Context, Member, bool) { calls.Add(1) }
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	managers := []*MembershipManager{
		{onChange: hook, closed: true, runCtx: context.Background()},
		{onChange: hook},
		{onChange: hook, runCtx: cancelled},
	}
	for _, m := range managers {
		m.fireChange(Member{NodeID: 2}, true)
		m.fireChangeWhenMember(Member{NodeID: 2}, false)
		m.wg.Wait()
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("hook fired %d times on a manager that is not running", n)
	}
}

// absentReconfigurer reports every replica as absent from the committed
// roster; the other Reconfigurer methods are not reached.
type absentReconfigurer struct{ Reconfigurer }

func (absentReconfigurer) IsMember(_, _ uint64) (bool, error) { return false, nil }

// TestFireChangeWhenMember_SupersededJoinFiresNothing: a join whose member is
// no longer listed (it left again before the roster committed the join) is
// superseded, so the hook is not fired for it.
func TestFireChangeWhenMember_SupersededJoinFiresNothing(t *testing.T) {
	var calls atomic.Int32
	m := &MembershipManager{
		onChange: func(context.Context, Member, bool) { calls.Add(1) },
		runCtx:   context.Background(),
		reconfig: absentReconfigurer{},
		members:  map[uint64]Member{},
	}
	m.fireChangeWhenMember(Member{NodeID: 7}, true)
	m.wg.Wait()
	if n := calls.Load(); n != 0 {
		t.Fatalf("hook fired %d times for a superseded join", n)
	}
}
