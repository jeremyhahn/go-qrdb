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
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"
)

// failoverLeaseTTL is the TTL of the lease kept alive across the failover.
// The keep-alive lands failoverRenewAfter into it, so the followers' copies
// would lapse failoverLeaseTTL-failoverRenewAfter seconds after it -- well
// before the deadline the keep-alive reported -- if a new leader trusted them.
const (
	failoverLeaseTTL   = 10
	failoverRenewAfter = 6 * time.Second
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
	// its full TTL; its stale follower copy had at most
	// failoverLeaseTTL-failoverRenewAfter seconds left.
	awaitClusterCondition(t, 5*time.Second, "new leader restarting the lease at its full TTL", func() error {
		got := httpLeaseTTL(t, c.ca, newLeader, lease.ID)
		if got.RemainingTTL < failoverLeaseTTL-1 {
			return &leaseNotAgedError{Remaining: got.RemainingTTL}
		}
		return nil
	})

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
