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

package service

import (
	"bytes"
	"context"
	"errors"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// Write guards
//
// A WriteGuard is a set of compares that must hold, at the log position of
// the entry being applied, for a write to apply at all. It rides on the
// request context rather than on every servicer method for the same reason
// the tenant does (tenant.WithTenantID): it is a property of WHICH database the
// request is scoped to, established once where that scope is resolved
// (pkg/server's namespaced views), and every write the request makes through
// any servicer in this package must carry it.
//
// That "any" is the property this design exists for. Every Raft proposal in
// this package goes through propose or proposeWithMode below, and both wrap
// the command with the context's guard (protocol.OpGuarded). A verb cannot
// forget the guard, because no verb proposes any other way -- which is
// exactly how the check-then-write fence this replaced kept failing: one
// method per wrapper at a time (go-qrdb#482).
//
// A context with no guard proposes the command unchanged, so every existing
// caller, and every command already in a Raft log, behaves exactly as
// before.
//
// A guarded command is only ever sent to a group every replica of which can
// apply it. A Host whose groups can hold replicas running different binaries
// implements FeatureGatedHost, and until its feature gate enables
// featuregate.GuardedWrites for the group the command is proposed unguarded:
// the database's drop fence is then the check made when the database is
// resolved, before the write, as it was before guarded writes existed. A
// Host that does not implement it -- standalone, transportless, or a test
// double -- has every replica running this binary.
// ---------------------------------------------------------------------------

// FeatureGatedHost is implemented by a Host whose Raft groups can hold
// replicas running binaries other than this one. FeatureEnabled reports
// whether every replica of shardID's group can apply commands that use
// feature (see pkg/cluster/featuregate).
type FeatureGatedHost interface {
	FeatureEnabled(shardID uint64, feature feature.Feature) bool
}

// ReplicaAdmitter is implemented by a Host (or RaftHost) whose groups gate
// features. AdmitReplica refuses -- with *featuregate.UnsupportedReplicaError
// -- to add a replica of shardID on the node at raftAddr when that node does
// not advertise every feature the group uses: go-quicraft replays the log to
// a new member, and it would diverge on the first entry it cannot apply.
// Every path that adds a replica asks before it changes anything.
type ReplicaAdmitter interface {
	AdmitReplica(shardID uint64, raftAddr string) error
}

// admitReplica asks h, when it gates features, whether a replica of shardID
// may be added on raftAddr.
func admitReplica(h any, shardID uint64, raftAddr string) error {
	if a, ok := h.(ReplicaAdmitter); ok {
		return a.AdmitReplica(shardID, raftAddr)
	}
	return nil
}

// WriteGuard is a precondition carried by every write proposed under a
// context it is attached to. The zero value guards nothing.
//
// It holds its compares already encoded (protocol.EncodeCompare) and
// validated, so attaching and merging guards cannot fail and wrapping a
// proposal is one call to protocol.EncodeGuarded.
type WriteGuard struct {
	compares [][]byte
}

// NewWriteGuard compiles compares into a WriteGuard. Only CompareVersion and
// CompareValue are accepted: a guard is evaluated on every state machine,
// several of which keep no revisions (see protocol.OpGuarded).
func NewWriteGuard(compares ...Compare) (WriteGuard, error) {
	if len(compares) == 0 {
		return WriteGuard{}, nil
	}
	encoded := make([][]byte, len(compares))
	for i, c := range compares {
		enc, err := protocol.EncodeCompare(c.Key, byte(c.Target), byte(c.Result), c.Value, c.Version, c.CreateRevision, c.ModRevision)
		if err != nil {
			return WriteGuard{}, &WriteGuardError{Err: err}
		}
		encoded[i] = enc
	}
	if err := protocol.ValidateGuard(encoded); err != nil {
		return WriteGuard{}, &WriteGuardError{Err: err}
	}
	return WriteGuard{compares: encoded}, nil
}

// IsZero reports whether g guards nothing.
func (g WriteGuard) IsZero() bool {
	return len(g.compares) == 0
}

// Equal reports whether g and other enforce exactly the same compares, in
// the same order.
func (g WriteGuard) Equal(other WriteGuard) bool {
	if len(g.compares) != len(other.compares) {
		return false
	}
	for i := range g.compares {
		if !bytes.Equal(g.compares[i], other.compares[i]) {
			return false
		}
	}
	return true
}

// merge returns a guard enforcing both g and other, without repeating a
// compare the two share.
func (g WriteGuard) merge(other WriteGuard) WriteGuard {
	merged := g.compares
	for _, c := range other.compares {
		if !containsFrame(merged, c) {
			merged = append(merged[:len(merged):len(merged)], c)
		}
	}
	return WriteGuard{compares: merged}
}

// wrap returns cmd wrapped with g. cmd must be a complete, non-empty command
// that is not itself guarded; every encoder in this package produces one.
func (g WriteGuard) wrap(cmd []byte) ([]byte, error) {
	guarded, err := protocol.EncodeGuarded(g.compares, cmd)
	if err != nil {
		return nil, &WriteGuardError{Err: err}
	}
	return guarded, nil
}

// writeGuardKey is the context key for the request's WriteGuard.
type writeGuardKey struct{}

// WithWriteGuard returns a context whose writes carry g in addition to any
// guard ctx already carries. Attaching the same compare twice -- a
// collection view over a namespaced KV view, both scoped to one database --
// is idempotent.
func WithWriteGuard(ctx context.Context, g WriteGuard) context.Context {
	if g.IsZero() {
		return ctx
	}
	existing := WriteGuardFrom(ctx)
	merged := existing.merge(g)
	if len(merged.compares) == len(existing.compares) {
		return ctx
	}
	return context.WithValue(ctx, writeGuardKey{}, merged)
}

// WriteGuardFrom returns the guard ctx carries, or the zero WriteGuard.
func WriteGuardFrom(ctx context.Context) WriteGuard {
	g, _ := ctx.Value(writeGuardKey{}).(WriteGuard)
	return g
}

// writeCheckKey is the context key for the request's write checks.
type writeCheckKey struct{}

// WithWriteCheck returns a context whose writes first run check, after any
// check ctx already carries; a check's error refuses the write before it is
// proposed. Unlike a WriteGuard it is decided on the proposing node at
// propose time, not by the state machine at apply time: it is for a policy
// the log cannot compare, such as a tenant's effective isolation policy.
// A nil check returns ctx unchanged.
func WithWriteCheck(ctx context.Context, check func(context.Context) error) context.Context {
	if check == nil {
		return ctx
	}
	existing, _ := ctx.Value(writeCheckKey{}).([]func(context.Context) error)
	checks := append(append([]func(context.Context) error(nil), existing...), check)
	return context.WithValue(ctx, writeCheckKey{}, checks)
}

// CheckWrite runs the write checks ctx carries (WithWriteCheck), in order,
// and returns the first error. Every proposal runs it (guardCommand).
func CheckWrite(ctx context.Context) error {
	checks, _ := ctx.Value(writeCheckKey{}).([]func(context.Context) error)
	for _, check := range checks {
		if err := check(ctx); err != nil {
			return err
		}
	}
	return nil
}

// guardedWriteCheckKey is the context key for the request's guarded write
// checks.
type guardedWriteCheckKey struct{}

// WithGuardedWriteCheck returns a context whose writes run check before they
// are proposed, after any check ctx already carries, and carry the guard it
// returns beside the context's own (WithWriteGuard); its error refuses the
// write. The guard is read at propose time and decided at apply time: check
// reads the values the write depends on, and the guard it returns compares
// them, in the write's own Raft command, on every replica. It is for a
// policy a concurrent change records in the log before it acts, such as a
// tenant's isolation epoch. Like every guard it is enforced only once
// featuregate.GuardedWrites is on (guardCommand). A nil check returns ctx
// unchanged.
func WithGuardedWriteCheck(ctx context.Context, check func(context.Context) (WriteGuard, error)) context.Context {
	if check == nil {
		return ctx
	}
	existing, _ := ctx.Value(guardedWriteCheckKey{}).([]func(context.Context) (WriteGuard, error))
	checks := append(append([]func(context.Context) (WriteGuard, error)(nil), existing...), check)
	return context.WithValue(ctx, guardedWriteCheckKey{}, checks)
}

// guardRefusalKey is the context key for the request's guard refusal
// mappers.
type guardRefusalKey struct{}

// WithGuardRefusal returns a context whose writes report a refused guard
// (*WriteGuardRefusedError) as the first non-nil error a mapper returns,
// marked ErrNotStored; a mapper returning nil leaves the refusal to the
// next, and the write reports it as before. It names the precondition a
// WithGuardedWriteCheck guard records. A nil mapper returns ctx unchanged.
func WithGuardRefusal(ctx context.Context, refused func(context.Context, *WriteGuardRefusedError) error) context.Context {
	if refused == nil {
		return ctx
	}
	existing, _ := ctx.Value(guardRefusalKey{}).([]func(context.Context, *WriteGuardRefusedError) error)
	mappers := append(append([]func(context.Context, *WriteGuardRefusedError) error(nil), existing...), refused)
	return context.WithValue(ctx, guardRefusalKey{}, mappers)
}

// mapGuardRefusal applies ctx's guard refusal mappers (WithGuardRefusal) to
// a proposal's result.
func mapGuardRefusal(ctx context.Context, result sm.Result, err error) (sm.Result, error) {
	if err != nil {
		return result, err
	}
	mappers, _ := ctx.Value(guardRefusalKey{}).([]func(context.Context, *WriteGuardRefusedError) error)
	if len(mappers) == 0 {
		return result, nil
	}
	key, refused := protocol.DecodeGuardRefusal(result.Data)
	if !refused {
		return result, nil
	}
	var wr *WriteGuardRefusedError
	if !errors.As(guardRefusalError(key), &wr) {
		return result, nil
	}
	for _, m := range mappers {
		if mapped := m(ctx, wr); mapped != nil {
			return sm.Result{}, markNotStored(mapped)
		}
	}
	return result, nil
}

// containsFrame reports whether frames already holds an encoded compare
// equal to f.
func containsFrame(frames [][]byte, f []byte) bool {
	for _, x := range frames {
		if bytes.Equal(x, f) {
			return true
		}
	}
	return false
}

// guardCommand wraps cmd with ctx's guard for proposal to shardID on host.
// It returns cmd unchanged when ctx carries no guard, or when host's feature
// gate has not enabled guarded writes for the group: a replica that cannot
// apply OpGuarded would refuse it while its peers apply it.
func guardCommand(ctx context.Context, host Host, shardID uint64, cmd []byte) ([]byte, error) {
	if err := CheckWrite(ctx); err != nil {
		return nil, err
	}
	g := WriteGuardFrom(ctx)
	checks, _ := ctx.Value(guardedWriteCheckKey{}).([]func(context.Context) (WriteGuard, error))
	for _, check := range checks {
		checked, err := check(ctx)
		if err != nil {
			return nil, err
		}
		g = g.merge(checked)
	}
	if g.IsZero() {
		return cmd, nil
	}
	if gated, ok := host.(FeatureGatedHost); ok && !gated.FeatureEnabled(shardID, featuregate.GuardedWrites) {
		return cmd, nil
	}
	return g.wrap(cmd)
}

// GuardsEnforced reports whether a write this service proposes carries its
// context's WriteGuard: always, unless the Host gates features and has not
// enabled guarded writes for the group (see guardCommand).
func (s *KVService) GuardsEnforced() bool {
	gated, ok := s.host.(FeatureGatedHost)
	return !ok || gated.FeatureEnabled(s.shardID, featuregate.GuardedWrites)
}

// propose is the single path every synchronous Raft write in this package
// takes: the command is wrapped with the context's WriteGuard, if any, and
// proposed to shardID.
func propose(ctx context.Context, host Host, shardID uint64, cmd []byte) (sm.Result, error) {
	guarded, err := guardCommand(ctx, host, shardID, cmd)
	if err != nil {
		return sm.Result{}, err
	}
	result, err := host.SyncPropose(ctx, shardID, guarded)
	return mapGuardRefusal(ctx, result, err)
}

// proposeWithMode is propose for a write with an explicit durability mode.
func proposeWithMode(ctx context.Context, host Host, shardID uint64, cmd []byte, mode writemode.Mode) (sm.Result, error) {
	guarded, err := guardCommand(ctx, host, shardID, cmd)
	if err != nil {
		return sm.Result{}, err
	}
	result, err := host.ProposeWithMode(ctx, shardID, guarded, mode)
	return mapGuardRefusal(ctx, result, err)
}
