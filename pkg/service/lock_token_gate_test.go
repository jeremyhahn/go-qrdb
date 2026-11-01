package service

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// TestLockTokenFieldRetired checks the lock record's fencing_token field is
// retired only once every replica of the group reads the token from the
// create revision: a host that gates nothing retires it at once, and a gated
// host retires it only when featuregate.LockCreateRevisionToken is active for
// the lock's own shard.
func TestLockTokenFieldRetired(t *testing.T) {
	t.Run("a host that gates nothing retires the field", func(t *testing.T) {
		if !lockTokenFieldRetired(&mockHost{}, 1) {
			t.Fatal("ungated host = false, want true")
		}
	})
	t.Run("an inactive gate keeps the field", func(t *testing.T) {
		host := &gatedHost{mockHost: &mockHost{}, enabled: map[uint64]bool{1: false}}
		if lockTokenFieldRetired(host, 1) {
			t.Fatal("inactive gate = true, want false")
		}
		if len(host.asked) != 1 || host.asked[0] != featuregate.LockCreateRevisionToken {
			t.Fatalf("the gate was asked %v, want [%s]", host.asked, featuregate.LockCreateRevisionToken)
		}
	})
	t.Run("an active gate retires the field for its own shard only", func(t *testing.T) {
		host := &gatedHost{mockHost: &mockHost{}, enabled: map[uint64]bool{1: true}}
		if !lockTokenFieldRetired(host, 1) {
			t.Fatal("active gate = false, want true")
		}
		if lockTokenFieldRetired(host, 2) {
			t.Fatal("a shard whose gate is not active = true, want false")
		}
		if len(host.asked) != 2 || host.asked[0] != featuregate.LockCreateRevisionToken {
			t.Fatalf("the gate was asked %v, want %s twice", host.asked, featuregate.LockCreateRevisionToken)
		}
	})
}

// A lock write is retried after a not-leader refusal or an unknown outcome,
// never after a failure proven final.
func TestLockWriteRetryable(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"not leader":      {&NotLeaderError{ShardID: 1, LeaderID: 2}, true},
		"outcome unknown": {&ProposeError{Err: context.DeadlineExceeded}, true},
		"encode refused":  {&EncodeRequestError{Err: context.Canceled}, false},
		"unmarked":        {context.Canceled, false},
		"nil":             {nil, false},
	}
	for name, tc := range cases {
		if got := lockWriteRetryable(tc.err); got != tc.want {
			t.Errorf("%s: lockWriteRetryable = %v, want %v", name, got, tc.want)
		}
	}
}
