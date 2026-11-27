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

//go:build integration

// lease_keepalive_test.go drives a real three-node qrdbd cluster through a
// leader failure while a lease is being kept alive. The leader renews a
// keep-alive in its own memory, so the followers' copies of the lease still
// carry the expiry of the last logged grant; a new leader must restart every
// lease at its full TTL rather than act on that stale copy, so that no lease
// expires earlier than its last keep-alive told the client.
package integration_test

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
)

// failoverLeaseTTL is the TTL of the lease kept alive across the failover.
// The keep-alive lands failoverRenewAfter into it, so the followers' copies
// would lapse failoverLeaseTTL-failoverRenewAfter seconds after it -- well
// before the deadline the keep-alive reported -- if a new leader trusted them.
const (
	failoverLeaseTTL   = 10
	failoverRenewAfter = 6 * time.Second
	// failoverSealReconfirm bounds how long a survivor refuses with
	// SEAL_STATE_UNCONFIRMED after the leader dies: the cluster seal monitor
	// serves for one lease past its last good records scan and scans every
	// poll interval, each scan bounded by two poll intervals, so one lease
	// plus a scan cycle covers a system shard that is still electing.
	failoverSealReconfirm = clusterseal.DefaultLease + 3*clusterseal.DefaultPollInterval
)

// TestLease_ThreeNode_KeepAliveSurvivesLeaderFailure kills the leader right
// after it renewed a lease in memory and checks, on the survivors, that:
//
//   - the key bound to the lease outlives the deadline the keep-alive reported;
//   - the new leader restarts the lease at its full TTL;
//   - a keep-alive sent to a follower renews the lease through the leader;
//   - once keep-alives stop, the new leader's sweeper revokes the lease and
//     every survivor deletes the key.
//
// leaseNotAgedError reports a lease whose remaining TTL is not yet the value
// the test is waiting for.
type leaseNotAgedError struct {
	Remaining int64
}

func (e *leaseNotAgedError) Error() string {
	return "lease has " + strconv.FormatInt(e.Remaining, 10) + "s remaining"
}

func TestLease_ThreeNode_KeepAliveSurvivesLeaderFailure(t *testing.T) {
	c := newRestartableCluster(t, leaseSMType)
	leaderPort := c.start(t, 1)

	const key = "lka/failover"
	lease := httpLeaseGrant(t, c.ca, leaderPort, failoverLeaseTTL)
	httpBatch(t, c.ca, leaderPort, []batchOp{{Type: "put", Key: key, Value: "v", LeaseID: lease.ID}})
	for _, port := range c.adminPorts {
		waitForKV(t, c.ca, port, key, []byte("v"), 60*time.Second)
	}

	// Let the lease age until the leader reports failoverRenewAfter spent.
	awaitClusterCondition(t, failoverRenewAfter+10*time.Second, "lease aged by the renew delay", func() error {
		got := httpLeaseTTL(t, c.ca, leaderPort, lease.ID)
		if aged := failoverLeaseTTL - got.RemainingTTL; time.Duration(aged)*time.Second < failoverRenewAfter {
			return &leaseNotAgedError{Remaining: got.RemainingTTL}
		}
		return nil
	})
	sent := time.Now()
	renewed := httpLeaseKeepAlive(t, c.ca, leaderPort, lease.ID)
	if renewed.ID != lease.ID || renewed.RemainingTTL < failoverLeaseTTL-1 {
		t.Fatalf("keep-alive on the leader = %+v, want lease %d back at its full %ds", renewed, lease.ID, failoverLeaseTTL)
	}
	reportedExpiry := sent.Add(time.Duration(renewed.RemainingTTL) * time.Second)

	leader := slices.Index(c.adminPorts, leaderPort)
	killed := time.Now()
	if err := c.cmds[leader].Process.Kill(); err != nil {
		t.Fatalf("kill leader node %d: %v", leader+1, err)
	}
	survivors := slices.Delete(slices.Clone(c.adminPorts), leader, leader+1)
	newLeader := waitForLeaderViaAdmin(t, c.ca, survivors, 60*time.Second)
	follower := survivors[0]
	if follower == newLeader {
		follower = survivors[1]
	}

	// The new leader's first sweep promotes it, restarting every lease at
	// its full TTL plus the failover grace; its stale follower copy had at
	// most failoverLeaseTTL-failoverRenewAfter seconds left. A survivor
	// whose cluster seal lease lapsed during the failover answers the
	// retryable SEAL_STATE_UNCONFIRMED until its next records scan
	// succeeds, so the window adds failoverSealReconfirm to the sweep's,
	// and leaseRestartedCheck allows for the lease aging meanwhile.
	awaitClusterCondition(t, 5*time.Second+failoverSealReconfirm, "new leader restarting the lease at its full TTL",
		leaseRestartedCheck(killed, time.Now, func() (leaseResult, error) {
			return httpLeaseTTLOrUnconfirmed(t, c.ca, newLeader, lease.ID)
		}))

	// No survivor may delete the key before the deadline the client was told.
	watch := time.NewTicker(250 * time.Millisecond)
	defer watch.Stop()
	reported := time.After(time.Until(reportedExpiry))
watching:
	for {
		for _, port := range survivors {
			if status := httpGetStatus(t, c.ca, port, key); status == http.StatusNotFound {
				t.Fatalf("port %d deleted %s %v before the deadline the keep-alive reported",
					port, key, time.Until(reportedExpiry).Round(time.Millisecond))
			}
		}
		select {
		case <-watch.C:
		case <-reported:
			break watching
		}
	}

	viaFollower := httpLeaseKeepAlive(t, c.ca, follower, lease.ID)
	if viaFollower.ID != lease.ID || viaFollower.RemainingTTL < failoverLeaseTTL-1 {
		t.Fatalf("keep-alive on follower port %d = %+v, want lease %d back at its full %ds",
			follower, viaFollower, lease.ID, failoverLeaseTTL)
	}

	// Stop renewing: the new leader's sweeper revokes the lease.
	for _, port := range survivors {
		waitForKeyAbsent(t, c.ca, port, key, 4*failoverLeaseTTL*time.Second)
	}
}

// leaseRestartedCheck is the condition that the new leader restarted the
// lease rather than trusting its stale follower copy. Promotion happens no
// earlier than killed and sets the lease to its full TTL plus the failover
// grace, after which it ages, also while the node refuses reads with
// SEAL_STATE_UNCONFIRMED. So a restarted lease still has at least
// failoverLeaseTTL minus the time since killed (less one second of
// whole-second rounding), while the stale copy has failoverRenewAfter less
// than that. A refusal from read is returned as is, so the poll retries it.
func leaseRestartedCheck(killed time.Time, now func() time.Time, read func() (leaseResult, error)) func() error {
	return func() error {
		got, err := read()
		if err != nil {
			return err
		}
		need := float64(failoverLeaseTTL-1) - now().Sub(killed).Seconds()
		if float64(got.RemainingTTL) < need {
			return &leaseNotAgedError{Remaining: got.RemainingTTL}
		}
		return nil
	}
}

// TestLease_RestartCheck_SlowSealReconfirm drives leaseRestartedCheck
// through a failover whose new leader keeps refusing with
// SEAL_STATE_UNCONFIRMED for 8 s after promotion, past the 6 s a fixed
// failoverLeaseTTL-1 threshold allowed, on a simulated clock: the restarted
// lease must pass on the first read after reconfirmation, and the stale
// follower copy must never pass.
func TestLease_RestartCheck_SlowSealReconfirm(t *testing.T) {
	const (
		election  = 3 * time.Second
		reconfirm = 8 * time.Second
		grace     = lease.DefaultFailoverGrace
		step      = 300 * time.Millisecond
	)
	killed := time.Unix(1_000_000, 0)
	promoted := killed.Add(election)
	confirmed := promoted.Add(reconfirm)
	remaining := func(expiry, at time.Time) int64 {
		return int64(expiry.Sub(at) / time.Second)
	}

	cases := []struct {
		name     string
		expiry   time.Time
		wantPass bool
	}{
		{"restarted at promotion", promoted.Add(failoverLeaseTTL*time.Second + grace), true},
		{"stale follower copy", killed.Add(failoverLeaseTTL*time.Second - failoverRenewAfter), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := killed
			check := leaseRestartedCheck(killed, func() time.Time { return clock }, func() (leaseResult, error) {
				if clock.Before(confirmed) {
					return leaseResult{}, &sealUnconfirmedError{Body: "simulated"}
				}
				return leaseResult{RemainingTTL: remaining(tc.expiry, clock)}, nil
			})
			var passedAt time.Time
			for ; clock.Before(confirmed.Add(5 * time.Second)); clock = clock.Add(step) {
				err := check()
				var unconfirmed *sealUnconfirmedError
				if clock.Before(confirmed) && !errors.As(err, &unconfirmed) {
					t.Fatalf("at %v before reconfirmation: err = %v, want the unconfirmed refusal", clock.Sub(killed), err)
				}
				if err == nil {
					passedAt = clock
					break
				}
			}
			switch {
			case tc.wantPass && passedAt.IsZero():
				t.Fatalf("restarted lease never passed the check after reconfirmation at %v", confirmed.Sub(killed))
			case tc.wantPass && passedAt.Sub(confirmed) > step:
				t.Fatalf("restarted lease passed at %v, want the first read after reconfirmation at %v",
					passedAt.Sub(killed), confirmed.Sub(killed))
			case !tc.wantPass && !passedAt.IsZero():
				t.Fatalf("stale follower copy passed the check at %v", passedAt.Sub(killed))
			}
		})
	}
}
