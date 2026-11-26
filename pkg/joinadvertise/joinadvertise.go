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

// Package joinadvertise implements clusterenroll.FeatureAdvertiser: a
// signing member publishing a pre-membership feature advertisement for a
// node it just issued a leaf to, so the operator's member add can admit it
// once a cluster feature has activated, even though the node itself is not
// running yet and cannot advertise for itself. The record is leased where
// the state machine has leases and a plain KV record where it has none.
//
// It is its own package, rather than living in pkg/clusterenroll (which
// defines the FeatureAdvertiser interface it implements) or
// pkg/server/lifecycle (which composes the Noise_NK join listener), because
// pkg/service already imports pkg/clusterenroll -- so clusterenroll cannot
// import service.LeaseServicer/service.MVCCKVServicer back -- and
// pkg/server/lifecycle imports pkg/server, so pkg/server cannot import
// lifecycle back either. Both composers (pkg/server/lifecycle's Noise_NK
// listener and pkg/server's REST /v1/enroll/join route) need the identical
// implementation, so it lives one level below both.
package joinadvertise

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// DefaultTTL bounds how long a pre-membership feature advertisement stays
// live before it must either be replaced by the node's own advertisement
// (once it is a member and running) or lapse. It matches the operator's
// usual window to run member add after a join -- generous enough for that by
// hand, short enough that an add that never happens does not leave a
// permanent, unused record behind.
const DefaultTTL = 15 * time.Minute

// Advertiser implements clusterenroll.FeatureAdvertiser over a node's own
// lease and MVCC-protocol KV surfaces -- the same two primitives
// pkg/cluster.Advertiser uses from the SDK side, called directly here
// because the signing member already holds them with no SDK round-trip to
// itself needed.
//
// When the state machine refuses the lease grant or the leased write as an
// operation it was not built for (a *service.CapabilityUnsupportedError, as
// the "memory" and "disk" state machines return), it writes the record with
// a plain KV Put instead. The fallback is chosen by that refusal, not by
// which services are wired: production always wires both lease and MVCC
// services, whatever the state machine type.
//
// It writes exactly once per join (a lease grant plus one PutWithLease, or
// one Put, never a keepalive loop): the record is a stopgap for the window before the
// joined node is added and starts advertising for itself, not a thing the
// signing member keeps alive on the joiner's behalf forever.
type Advertiser struct {
	lease service.LeaseServicer
	mvcc  service.MVCCKVServicer
	kv    KVWriter
	ttl   time.Duration
}

// KVWriter is the plain KV write an Advertiser falls back to when the state
// machine refuses the leased write with a *service.CapabilityUnsupportedError
// (or when no lease and MVCC services are wired at all).
type KVWriter interface {
	Put(ctx context.Context, key string, value []byte) error
}

// New returns an Advertiser that writes a leased record when leaseSvc and
// mvcc are both set, and keeps kv as its fallback: if the state machine
// refuses the lease grant or the leased write with a
// *service.CapabilityUnsupportedError (it was not built with leases or MVCC,
// as on "memory"/"disk" state machines), the same record is written with
// kv.Put and no lease, which the joined member deletes once its release is
// recorded (featuregate.Gate.StartAdmission). Any other lease or write error
// is returned as is, with no fallback. When either of leaseSvc or mvcc is nil
// it returns an Advertiser that writes with kv.Put only. It returns nil only
// when kv is nil as well. A nil
// *Advertiser is intentionally NOT assigned to a
// clusterenroll.FeatureAdvertiser variable by callers (that would produce a
// non-nil interface wrapping a nil pointer); callers check New's result
// before assigning, so HandleJoinRequest sees a true nil interface and skips
// advertising silently, exactly as when none was configured at all.
func New(leaseSvc service.LeaseServicer, mvcc service.MVCCKVServicer, kv KVWriter) *Advertiser {
	if leaseSvc != nil && mvcc != nil {
		return &Advertiser{lease: leaseSvc, mvcc: mvcc, kv: kv, ttl: DefaultTTL}
	}
	if kv != nil {
		return &Advertiser{kv: kv}
	}
	return nil
}

// AdvertiseJoiner implements clusterenroll.FeatureAdvertiser. features is
// filtered to the names this binary's own featuregate.Supported() knows --
// an unknown name is dropped, never trusted onto the wire of the
// advertisement record -- so the result is bounded by that fixed set
// regardless of how many names the joiner claimed.
func (a *Advertiser) AdvertiseJoiner(ctx context.Context, replicaID uint64, raftAddress string, features []string, releaseVersion string) error {
	known := featuregate.SupportedNames()
	filtered := make([]string, 0, len(features))
	for _, f := range features {
		if slices.Contains(known, f) {
			filtered = append(filtered, f)
		}
	}

	member := cluster.Member{NodeID: replicaID, Address: raftAddress, Features: filtered, ReleaseVersion: releaseVersion}
	data, err := json.Marshal(member)
	if err != nil {
		return &EncodeError{Err: err}
	}

	key := featuregate.AdvertisementPrefix + strconv.FormatUint(replicaID, 10)
	if a.lease == nil {
		return a.putUnleased(ctx, key, data)
	}
	granted, err := a.lease.Grant(ctx, int64(a.ttl.Seconds()))
	if err != nil {
		if a.refusedByStateMachine(err) {
			return a.putUnleased(ctx, key, data)
		}
		return &LeaseError{Err: err}
	}
	if _, err := a.mvcc.PutWithLease(ctx, key, data, granted.ID); err != nil {
		_ = a.lease.Revoke(ctx, granted.ID)
		if a.refusedByStateMachine(err) {
			return a.putUnleased(ctx, key, data)
		}
		return &WriteError{Err: err}
	}
	return nil
}

// refusedByStateMachine reports whether err is the state machine's refusal
// of an operation it was not built for and a plain KV fallback is wired.
func (a *Advertiser) refusedByStateMachine(err error) bool {
	var unsupported *service.CapabilityUnsupportedError
	return a.kv != nil && errors.As(err, &unsupported)
}

// putUnleased writes the record with a plain KV Put and no lease.
func (a *Advertiser) putUnleased(ctx context.Context, key string, data []byte) error {
	if err := a.kv.Put(ctx, key, data); err != nil {
		return &WriteError{Err: err}
	}
	return nil
}

// EncodeError wraps a failure encoding the advertisement record.
type EncodeError struct{ Err error }

// Error returns a human-readable description.
func (e *EncodeError) Error() string {
	return "joinadvertise: encode advertisement: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EncodeError) Unwrap() error { return e.Err }

// LeaseError wraps a failure granting the advertisement's lease.
type LeaseError struct{ Err error }

// Error returns a human-readable description.
func (e *LeaseError) Error() string {
	return "joinadvertise: grant advertisement lease: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *LeaseError) Unwrap() error { return e.Err }

// WriteError wraps a failure writing the advertisement record.
type WriteError struct{ Err error }

// Error returns a human-readable description.
func (e *WriteError) Error() string {
	return "joinadvertise: write advertisement: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *WriteError) Unwrap() error { return e.Err }
