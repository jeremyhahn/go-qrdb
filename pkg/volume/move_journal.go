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
		next := *current
		next.NodesCopied = append([]string(nil), current.NodesCopied...)
		next.NodesPending = append([]string(nil), current.NodesPending...)
		if err := apply(&next); err != nil {
			return nil, err
		}
		next.UpdatedAt = time.Now()
		data, err := json.Marshal(next)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}
		resp, err := c.tx.Execute(ctx,
			[]lock.Compare{
				{Key: []byte(MoveKey(tenantID)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: raw},
			},
			[]lock.Op{
				{Type: lock.OpPut, Key: []byte(MoveKey(tenantID)), Value: data},
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
// NodesCopied and the step is released. With no node left pending the move
// is copied, ready for cut over; otherwise it stays copying. A node that
// does not hold the step is refused with *MoveConflictError.
func (c *Catalog) CompleteMoveStep(ctx context.Context, tenantID, nodeID string) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if m.StepNode == "" || m.StepNode != nodeID {
			return &MoveConflictError{TenantID: tenantID, NodeID: nodeID, Reason: "node does not hold the move step"}
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
		m.StepNode = ""
		m.StepAttempts = 0
		m.Error = ""
		if len(m.NodesPending) == 0 {
			m.NodesPending = nil
			m.State = MoveStateCopied
		} else {
			m.State = MoveStateCopying
		}
		return nil
	})
}

// FailMoveStep records a failed attempt of nodeID's step of a tenant move
// ("The move, phase by phase" step 3, decision Q6 A's bounded retry): the
// attempt is counted and reason kept in Error. Once maxAttempts attempts
// failed (DefaultMoveStepAttempts when maxAttempts <= 0) the move is
// aborting and the step released; the node stays pending. A node that does
// not hold the step is refused with *MoveConflictError.
func (c *Catalog) FailMoveStep(ctx context.Context, tenantID, nodeID, reason string, maxAttempts int) (*Move, error) {
	if nodeID == "" {
		return nil, &InvalidMoveRequestError{Reason: "empty node id"}
	}
	if maxAttempts <= 0 {
		maxAttempts = DefaultMoveStepAttempts
	}
	return c.updateMove(ctx, tenantID, func(m *Move) error {
		if m.StepNode == "" || m.StepNode != nodeID {
			return &MoveConflictError{TenantID: tenantID, NodeID: nodeID, Reason: "node does not hold the move step"}
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
