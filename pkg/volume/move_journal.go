// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// updateMove applies one transition to the move journal of tenantID as a
// compare-and-swap on the journal's raw value (decision Q5). apply edits a
// copy of the journal; its error is returned as is and nothing is written.
// It retries up to maxCASRetries times when the journal changed under it,
// then returns *MoveJournalConflictError.
func (c *Catalog) updateMove(ctx context.Context, tenantID string, apply func(*Move) error) (*Move, error) {
	return c.casMove(ctx, tenantID, func(m *Move) (*moveRelease, error) {
		return nil, apply(m)
	})
}

// moveRelease is the end of a move: Bytes are released from Pool's
// reservation and the journal is deleted, in one transaction.
type moveRelease struct {
	Pool  string
	Bytes int64
}

// cloneMove copies m with its own node slices.
func cloneMove(m *Move) Move {
	next := *m
	next.NodesCopied = append([]string(nil), m.NodesCopied...)
	next.NodesPending = append([]string(nil), m.NodesPending...)
	next.NodesRetired = append([]string(nil), m.NodesRetired...)
	return next
}

// casMove is updateMove whose apply may end the move: a non-nil
// moveRelease makes the transaction lower the pool's ReservedBytes by its
// Bytes (floor 0, as Release) and delete the journal, comparing both the
// pool's and the journal's raw values. The final Move is returned.
func (c *Catalog) casMove(ctx context.Context, tenantID string, apply func(*Move) (*moveRelease, error)) (*Move, error) {
	if tenantID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty tenant id"}
	}
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		current, raw, err := c.readMove(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, &MoveNotFoundError{TenantID: tenantID}
		}
		next := cloneMove(current)
		release, err := apply(&next)
		if err != nil {
			return nil, err
		}
		next.UpdatedAt = time.Now()
		cmps, ops, err := c.moveWrite(ctx, raw, &next, release)
		if err != nil {
			return nil, err
		}
		resp, err := c.tx.Execute(ctx, cmps, ops, nil)
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return &next, nil
		}
	}
	return nil, &MoveJournalConflictError{TenantID: tenantID}
}

// moveWrite builds casMove's transaction: a put of next compared against
// the journal's raw value, or, with a release, the pool's lowered
// reservation and the journal's delete compared against both raw values.
func (c *Catalog) moveWrite(ctx context.Context, raw []byte, next *Move, release *moveRelease) ([]lock.Compare, []lock.Op, error) {
	key := []byte(MoveKey(next.TenantID))
	moveCmp := lock.Compare{Key: key, Target: lock.CompareValue, Result: lock.CompareEqual, Value: raw}
	if release == nil {
		data, err := json.Marshal(next)
		if err != nil {
			return nil, nil, &EncodeError{Err: err}
		}
		return []lock.Compare{moveCmp}, []lock.Op{{Type: lock.OpPut, Key: key, Value: data}}, nil
	}
	pool, poolRaw, err := c.getPoolRaw(ctx, release.Pool)
	if err != nil {
		return nil, nil, err
	}
	updated := *pool
	updated.ReservedBytes -= release.Bytes
	if updated.ReservedBytes < 0 {
		updated.ReservedBytes = 0
	}
	updated.UpdatedAt = next.UpdatedAt
	poolData, err := json.Marshal(updated)
	if err != nil {
		return nil, nil, &EncodeError{Err: err}
	}
	poolKey := []byte(PoolKey(release.Pool))
	return []lock.Compare{
		{Key: poolKey, Target: lock.CompareValue, Result: lock.CompareEqual, Value: poolRaw},
		moveCmp,
	}, []lock.Op{
		{Type: lock.OpPut, Key: poolKey, Value: poolData},
		{Type: lock.OpDelete, Key: key},
	}, nil
}

// GrantMoveStep grants the next node its step of a tenant move ("The move,
// phase by phase" step 2): the first pending node becomes StepNode and the
// move is copying. At most one node holds the step (decision Q1 B). A step
// already granted is returned unchanged with no write, so a reconciler that
// took over after a crash re-runs it. A move in another state than admitted
// or copying, or with no pending node, is refused with *MoveConflictError.
func (c *Catalog) GrantMoveStep(ctx context.Context, tenantID string) (*Move, error) {
	if tenantID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty tenant id"}
	}
	current, _, err := c.readMove(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &MoveNotFoundError{TenantID: tenantID}
	}
	if current.StepNode != "" && current.State == MoveStateCopying {
		return current, nil
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if m.State != MoveStateAdmitted && m.State != MoveStateCopying {
			return &MoveConflictError{TenantID: tenantID, Reason: "move is " + m.State + ": no step to grant"}
		}
		if m.StepNode != "" {
			return nil
		}
		if m.CancelRequested {
			return &MoveConflictError{TenantID: tenantID, Reason: "move is cancelling: no step to grant"}
		}
		if len(m.NodesPending) == 0 {
			return &MoveConflictError{TenantID: tenantID, Reason: "move is " + m.State + ": no step to grant"}
		}
		m.StepNode = m.NodesPending[0]
		m.StepAttempts = 0
		m.State = MoveStateCopying
		return nil
	})
}

// CompleteMoveStep records that nodeID finished its step of a tenant move
// ("The move, phase by phase" step 3): the node leaves NodesPending for
// NodesCopied, so the destination is authoritative for it, but it keeps the
// step settling (StepSettling) until AckMoveStep after it reopened and
// caught up there; the ack then makes the move copied (no node left
// pending), aborting (a cancel arrived) or leaves it copying. A node that
// does not hold the step, or a move no longer copying (cancelled), is
// refused with *MoveConflictError.
func (c *Catalog) CompleteMoveStep(ctx context.Context, tenantID, nodeID string) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if err := holdsStep(m, MoveStateCopying, nodeID); err != nil {
			return err
		}
		if m.StepSettling {
			return nil // already recorded: a retry after a lost reply
		}
		pending := make([]string, 0, len(m.NodesPending))
		for _, n := range m.NodesPending {
			if n != nodeID {
				pending = append(pending, n)
			}
		}
		m.NodesPending = pending
		m.NodesCopied = append(m.NodesCopied, nodeID)
		sort.Strings(m.NodesCopied)
		// The destination is authoritative for nodeID from here, but the
		// node keeps the step until AckMoveStep: it has not yet reopened
		// and caught up on it.
		m.StepSettling = true
		return nil
	})
}

// AckMoveStep releases nodeID's settling step (StepSettling) after its
// SettleMoveStep succeeded. A copy step then moves on as CompleteMoveStep
// did before the hold: aborting on a cancel requested meanwhile, copied
// when no node is pending, else copying (the next node's grant follows).
// A copied-abort step releases its node; the last node's ack releases the
// ToPool reservation and deletes the journal.
func (c *Catalog) AckMoveStep(ctx context.Context, tenantID, nodeID string) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	return c.casMove(ctx, tenantID, func(m *Move) (*moveRelease, error) {
		if m.StepNode == "" || m.StepNode != nodeID || !m.StepSettling {
			return nil, &MoveConflictError{TenantID: m.TenantID, NodeID: nodeID, Reason: "node does not hold a settling move step"}
		}
		m.StepNode = ""
		m.StepAttempts = 0
		m.StepSettling = false
		switch m.State {
		case MoveStateCopying:
			if m.CancelRequested {
				m.CancelRequested = false
				m.State = MoveStateAborting
				return nil, nil
			}
			m.Error = ""
			if len(m.NodesPending) == 0 {
				m.NodesPending = nil
				m.State = MoveStateCopied
			}
			return nil, nil
		case MoveStateAborting:
			if len(m.NodesCopied) > 0 || len(m.NodesPending) > 0 {
				return nil, nil
			}
			m.NodesCopied = nil
			m.NodesPending = nil
			return &moveRelease{Pool: m.ToPool, Bytes: m.ReservedBytes}, nil
		default:
			return nil, &MoveConflictError{TenantID: m.TenantID, NodeID: nodeID, Reason: "move is " + m.State + ": no step to settle"}
		}
	})
}

// FailMoveStep records a failed attempt of nodeID's step of a tenant move
// ("The move, phase by phase" step 3, decision Q6 A's bounded retry): the
// attempt is counted and reason kept in Error. Once maxAttempts attempts
// failed (DefaultMoveStepAttempts when maxAttempts <= 0) the move is
// aborting and the step released; the node stays pending. A node that does
// not hold the step, or a move no longer copying (cancelled), is refused
// with *MoveConflictError.
func (c *Catalog) FailMoveStep(ctx context.Context, tenantID, nodeID, reason string, maxAttempts int) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultMoveStepAttempts
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if err := holdsStep(m, MoveStateCopying, nodeID); err != nil {
			return err
		}
		if m.StepSettling {
			return &MoveConflictError{TenantID: m.TenantID, NodeID: nodeID, Reason: "the move step is recorded and settling"}
		}
		if m.CancelRequested {
			m.CancelRequested = false
			m.State = MoveStateAborting
			m.StepNode = ""
			m.StepAttempts = 0
			m.Error = "cancelled by operator"
			return nil
		}
		m.StepAttempts++
		m.Error = reason
		if m.StepAttempts >= maxAttempts {
			m.State = MoveStateAborting
			m.StepNode = ""
		}
		return nil
	})
}

// holdsStep refuses, with *MoveConflictError, a step settled by a node that
// does not hold it or on a move no longer in state: a copy step granted
// before CancelMove is refused once the move is aborting.
func holdsStep(m *Move, state, nodeID string) error {
	if m.State != state {
		return &MoveConflictError{TenantID: m.TenantID, NodeID: nodeID, Reason: "move is " + m.State + ", not " + state}
	}
	if m.StepNode == "" || m.StepNode != nodeID {
		return &MoveConflictError{TenantID: m.TenantID, NodeID: nodeID, Reason: "node does not hold the move step"}
	}
	return nil
}

// without returns list less node.
func without(list []string, node string) []string {
	out := make([]string, 0, len(list))
	for _, n := range list {
		if n != node {
			out = append(out, n)
		}
	}
	return out
}

// contains reports whether list holds node.
func contains(list []string, node string) bool {
	for _, n := range list {
		if n == node {
			return true
		}
	}
	return false
}

// CutOverMove is a tenant move's commit point ("The move, phase by phase"
// step 4, docs/decisions/volume-pool-tenant-moves.md). In one transaction,
// comparing the assignment's and the journal's raw values, it rewrites the
// assignment to name ToPool with the same Path and the move's
// ReservedBytes, and puts the journal in MoveStateCutover with the source
// assignment's ReservedBytes recorded as SourceReservedBytes. Before it
// the move rolls back; after it the move only goes forward.
//
// A journal already cut over is returned with no write. A move that is not
// copied, still has a node pending, or holds a step, or an assignment no
// longer naming FromPool, is refused with *MoveConflictError; an
// undecodable assignment returns *CorruptRecordError. A CAS miss is retried
// up to maxCASRetries times, then *MoveJournalConflictError.
func (c *Catalog) CutOverMove(ctx context.Context, tenantID string) (*Move, error) {
	if tenantID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty tenant id"}
	}
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		current, raw, err := c.readMove(ctx, tenantID)
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, &MoveNotFoundError{TenantID: tenantID}
		}
		if current.State == MoveStateCutover {
			return current, nil
		}
		if current.State != MoveStateCopied || len(current.NodesPending) != 0 || current.StepNode != "" {
			return nil, &MoveConflictError{TenantID: tenantID, Reason: "move is " + current.State + ": not ready for cut over"}
		}
		assignRaw, err := c.kv.Get(ctx, AssignmentKey(tenantID))
		if err != nil {
			return nil, err
		}
		if assignRaw == nil {
			return nil, &AssignmentNotFoundError{TenantID: tenantID}
		}
		var assignment Assignment
		if err := json.Unmarshal(assignRaw, &assignment); err != nil {
			return nil, &CorruptRecordError{Key: AssignmentKey(tenantID), Err: err}
		}
		if assignment.Pool != current.FromPool {
			return nil, &MoveConflictError{TenantID: tenantID, Reason: "assignment no longer names the source pool"}
		}
		next := cloneMove(current)
		next.State = MoveStateCutover
		next.SourceReservedBytes = assignment.ReservedBytes
		next.Error = ""
		next.UpdatedAt = time.Now()
		moved := Assignment{TenantID: tenantID, Pool: current.ToPool, Path: assignment.Path, ReservedBytes: current.ReservedBytes}
		assignData, err := json.Marshal(moved)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}
		moveData, err := json.Marshal(next)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}
		resp, err := c.tx.Execute(ctx,
			[]lock.Compare{
				{Key: []byte(AssignmentKey(tenantID)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: assignRaw},
				{Key: []byte(MoveKey(tenantID)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: raw},
			},
			[]lock.Op{
				{Type: lock.OpPut, Key: []byte(AssignmentKey(tenantID)), Value: assignData},
				{Type: lock.OpPut, Key: []byte(MoveKey(tenantID)), Value: moveData},
			},
			nil,
		)
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return &next, nil
		}
	}
	return nil, &MoveJournalConflictError{TenantID: tenantID}
}

// MarkMoveTenantRecorded records that the tenant record names the move's
// ToPool (the wired MoveCutoverRecorder ran). Only a move cut over is
// marked; any other is refused with *MoveConflictError.
func (c *Catalog) MarkMoveTenantRecorded(ctx context.Context, tenantID string) (*Move, error) {
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if m.State != MoveStateCutover {
			return &MoveConflictError{TenantID: tenantID, Reason: "move is " + m.State + ": not cut over"}
		}
		m.TenantRecorded = true
		return nil
	})
}

// grantPhaseStep grants the next node of a retire or abort step: in state,
// pick names the node or refuses. A step already granted is returned
// unchanged with no write, so a reconciler that took over re-runs it.
func (c *Catalog) grantPhaseStep(ctx context.Context, tenantID, state string, pick func(*Move) (string, error)) (*Move, error) {
	if tenantID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty tenant id"}
	}
	current, _, err := c.readMove(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &MoveNotFoundError{TenantID: tenantID}
	}
	if current.State == state && current.StepNode != "" {
		return current, nil
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if m.State != state {
			return &MoveConflictError{TenantID: tenantID, Reason: "move is " + m.State + ": no " + state + " step to grant"}
		}
		if m.StepNode != "" {
			return nil
		}
		node, err := pick(m)
		if err != nil {
			return err
		}
		m.StepNode = node
		m.StepAttempts = 0
		return nil
	})
}

// failPhaseStep records a failed attempt of nodeID's retire or abort step:
// the attempt is counted and reason kept in Error; the node keeps the step,
// so the next pass retries it, with no bound.
func (c *Catalog) failPhaseStep(ctx context.Context, tenantID, nodeID, state, reason string) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if err := holdsStep(m, state, nodeID); err != nil {
			return err
		}
		m.StepAttempts++
		m.Error = reason
		return nil
	})
}

// GrantRetireStep grants the next node its retire step of a tenant move cut
// over (decision Q7 A): the first node of NodesCopied not in NodesRetired.
// A move not cut over, whose tenant record is not yet moved
// (TenantRecorded), or with every node retired is refused with
// *MoveConflictError.
func (c *Catalog) GrantRetireStep(ctx context.Context, tenantID string) (*Move, error) {
	return c.grantPhaseStep(ctx, tenantID, MoveStateCutover, func(m *Move) (string, error) {
		if !m.TenantRecorded {
			return "", &MoveConflictError{TenantID: tenantID, Reason: "tenant record does not name the destination pool yet"}
		}
		for _, n := range m.NodesCopied {
			if !contains(m.NodesRetired, n) {
				return n, nil
			}
		}
		return "", &MoveConflictError{TenantID: tenantID, Reason: "move is cutover: no node left to retire"}
	})
}

// CompleteRetireStep records that nodeID removed its source copy: the node
// joins NodesRetired. Once every node of NodesCopied retired, one
// transaction releases SourceReservedBytes from FromPool and deletes the
// journal; the final Move is returned. A node that does not hold the step
// is refused with *MoveConflictError.
func (c *Catalog) CompleteRetireStep(ctx context.Context, tenantID, nodeID string) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	return c.casMove(ctx, tenantID, func(m *Move) (*moveRelease, error) {
		if err := holdsStep(m, MoveStateCutover, nodeID); err != nil {
			return nil, err
		}
		m.NodesRetired = append(m.NodesRetired, nodeID)
		sort.Strings(m.NodesRetired)
		m.StepNode = ""
		m.StepAttempts = 0
		m.Error = ""
		for _, n := range m.NodesCopied {
			if !contains(m.NodesRetired, n) {
				return nil, nil
			}
		}
		return &moveRelease{Pool: m.FromPool, Bytes: m.SourceReservedBytes}, nil
	})
}

// FailRetireStep records a failed attempt of nodeID's retire step. The move
// stays cut over and the step is retried every pass with no bound: after
// cut over a move never aborts.
func (c *Catalog) FailRetireStep(ctx context.Context, tenantID, nodeID, reason string) (*Move, error) {
	return c.failPhaseStep(ctx, tenantID, nodeID, MoveStateCutover, reason)
}

// CancelMove is the operator's cancel of a tenant move (decision Q8 A): a
// move admitted, copying or copied goes to aborting, its step released and
// Error "cancelled by operator"; a step a node already holds is then
// refused at CompleteMoveStep and FailMoveStep. A move already aborting is
// returned unchanged. A move cut over is past its commit point and refused
// with *MoveConflictError; no journal returns *MoveNotFoundError and an
// empty tenant *InvalidMoveRequestError.
func (c *Catalog) CancelMove(ctx context.Context, tenantID string) (*Move, error) {
	if tenantID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty tenant id"}
	}
	current, _, err := c.readMove(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, &MoveNotFoundError{TenantID: tenantID}
	}
	if current.State == MoveStateAborting || current.CancelRequested {
		return current, nil
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if m.CancelRequested {
			return nil
		}
		switch m.State {
		case MoveStateCopying:
			if m.StepNode != "" {
				// A copy is running: its holder awaits it, and the step's
				// completion or failure turns the move aborting with the
				// node's real copied state, so abort copies the
				// destination's writes back when the destination went live.
				m.CancelRequested = true
				m.Error = "cancelled by operator"
				return nil
			}
			m.State = MoveStateAborting
			m.StepAttempts = 0
			m.Error = "cancelled by operator"
			return nil
		case MoveStateAdmitted, MoveStateCopied:
			m.State = MoveStateAborting
			m.StepNode = ""
			m.StepAttempts = 0
			m.Error = "cancelled by operator"
			return nil
		case MoveStateAborting:
			return nil
		default:
			return &MoveConflictError{TenantID: tenantID, Reason: "move is " + m.State + ": past its commit point, cannot cancel"}
		}
	})
}

// GrantAbortStep grants the next node its abort step of a move rolled back
// before cut over ("Failure and rollback"): nodes of NodesCopied first, one
// at a time (decision Q1 B), then nodes of NodesPending (a partial
// destination copy). A move not aborting is refused with
// *MoveConflictError.
func (c *Catalog) GrantAbortStep(ctx context.Context, tenantID string) (*Move, error) {
	return c.grantPhaseStep(ctx, tenantID, MoveStateAborting, func(m *Move) (string, error) {
		if len(m.NodesCopied) > 0 {
			return m.NodesCopied[0], nil
		}
		if len(m.NodesPending) > 0 {
			return m.NodesPending[0], nil
		}
		return "", &MoveConflictError{TenantID: tenantID, Reason: "move is aborting: no node left to abort"}
	})
}

// CompleteAbortStep records that nodeID removed its destination copy: the
// node leaves NodesCopied or NodesPending. Once both are empty, one
// transaction releases the move's ReservedBytes from ToPool and deletes the
// journal; the assignment is never written. The final Move is returned. A
// node that does not hold the step is refused with *MoveConflictError.
func (c *Catalog) CompleteAbortStep(ctx context.Context, tenantID, nodeID string) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	return c.casMove(ctx, tenantID, func(m *Move) (*moveRelease, error) {
		if err := holdsStep(m, MoveStateAborting, nodeID); err != nil {
			return nil, err
		}
		m.NodesCopied = without(m.NodesCopied, nodeID)
		m.NodesPending = without(m.NodesPending, nodeID)
		m.StepNode = ""
		m.StepAttempts = 0
		if len(m.NodesCopied) > 0 || len(m.NodesPending) > 0 {
			return nil, nil
		}
		m.NodesCopied = nil
		m.NodesPending = nil
		return &moveRelease{Pool: m.ToPool, Bytes: m.ReservedBytes}, nil
	})
}

// FailAbortStep records a failed attempt of nodeID's abort step. The node
// keeps its list and the step, retried every pass with no bound.
func (c *Catalog) FailAbortStep(ctx context.Context, tenantID, nodeID, reason string) (*Move, error) {
	return c.failPhaseStep(ctx, tenantID, nodeID, MoveStateAborting, reason)
}

// scanMoves returns every move journal, sorted by tenant. A record that
// fails to decode is skipped with a logged warning: one corrupt journal must
// not stop every other tenant's move.
func (c *Catalog) scanMoves(ctx context.Context) ([]Move, error) {
	raw, err := c.kv.Scan(ctx, MoveKeyPrefix)
	if err != nil {
		return nil, err
	}
	moves := make([]Move, 0, len(raw))
	for key, data := range raw {
		var m Move
		if err := json.Unmarshal(data, &m); err != nil {
			c.logger.Warn("volume: skipping corrupt move journal", slog.String("key", key), slog.String("error", err.Error()))
			continue
		}
		moves = append(moves, m)
	}
	sort.Slice(moves, func(i, j int) bool { return moves[i].TenantID < moves[j].TenantID })
	return moves, nil
}
