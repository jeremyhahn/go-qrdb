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

// Package clustersealtest provides a faithful clusterseal.Sealer double for
// the services that seal through one.
package clustersealtest

import (
	"context"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// Sealer behaves as clusterseal.Monitor does from a service's point of view:
// an inactive Sealer refuses Seal and Clear with clusterseal.ErrInactive;
// Seal records a sealed epoch (a repeat seal of a sealed record writes
// nothing) and then enforces it on this node through OnSeal, as the monitor
// enforces through its Enforcer; Clear records a clear epoch and unseals
// nothing -- and, as the monitor does, refuses the node barrier with
// seal.ErrSealed while this node's own node barrier is sealed (SetNodeSealed);
// Refuses reports a sealed record. SealErr, SealRefusal and ClearErr inject
// the failures the monitor can return.
type Sealer struct {
	mu      sync.Mutex
	active  bool
	records map[clusterseal.Barrier]clusterseal.Record
	sealErr error
	// sealRefusal is a definite refusal: the record is not written and
	// nothing is enforced, as the monitor answers a lost seal-epoch race.
	sealRefusal error
	clearErr    error
	onSeal      func(clusterseal.Barrier) error
	local       clusterseal.Local
	// nodeSealed is this node's own node barrier state, which the monitor
	// reads through its Enforcer before clearing the node record.
	nodeSealed bool
	seals      []clusterseal.Barrier
	clears     []clusterseal.Barrier
	by         []clusterseal.Principal
}

var _ clusterseal.Sealer = (*Sealer)(nil)

// New returns a Sealer, active or not, reporting this node as serving.
func New(active bool) *Sealer {
	return &Sealer{active: active, records: map[clusterseal.Barrier]clusterseal.Record{}, local: clusterseal.LocalServing}
}

// SetNodeSealed sets whether this node's own node barrier is sealed.
func (s *Sealer) SetNodeSealed(sealed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeSealed = sealed
}

// OnSeal sets the local enforcement Seal runs after recording.
func (s *Sealer) OnSeal(f func(clusterseal.Barrier) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSeal = f
}

// SetSealErr makes Seal return err after it has enforced locally, as the
// monitor does for an unconfirmed record.
func (s *Sealer) SetSealErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealErr = err
}

// SetSealRefusal makes Seal refuse with err before it writes a record or
// enforces anything, as the monitor does when the record definitely was not
// written (*clusterseal.SealEpochConflictError): this node keeps its keys.
func (s *Sealer) SetSealRefusal(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealRefusal = err
}

// SetClearErr makes Clear fail with err and record nothing.
func (s *Sealer) SetClearErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearErr = err
}

// SetLocal sets the Local Status reports.
func (s *Sealer) SetLocal(l clusterseal.Local) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.local = l
}

// Put stores a record as if another node had written it.
func (s *Sealer) Put(rec clusterseal.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[rec.Barrier] = rec
}

// Record returns b's record.
func (s *Sealer) Record(b clusterseal.Barrier) clusterseal.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.records[b]
}

// Seals returns every barrier Seal was called for, in order.
func (s *Sealer) Seals() []clusterseal.Barrier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]clusterseal.Barrier(nil), s.seals...)
}

// Clears returns every barrier Clear was called for, in order.
func (s *Sealer) Clears() []clusterseal.Barrier {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]clusterseal.Barrier(nil), s.clears...)
}

// Principals returns who called Seal and Clear, in order.
func (s *Sealer) Principals() []clusterseal.Principal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]clusterseal.Principal(nil), s.by...)
}

// Active implements clusterseal.Sealer.
func (s *Sealer) Active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Seal implements clusterseal.Sealer.
func (s *Sealer) Seal(_ context.Context, b clusterseal.Barrier, by clusterseal.Principal) (clusterseal.Record, error) {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return clusterseal.Record{}, clusterseal.ErrInactive
	}
	s.seals = append(s.seals, b)
	s.by = append(s.by, by)
	if s.sealRefusal != nil {
		err := s.sealRefusal
		s.mu.Unlock()
		return clusterseal.Record{}, err
	}
	rec := s.records[b]
	if !rec.Sealed {
		rec = clusterseal.Record{Barrier: b, Epoch: rec.Epoch + 1, Sealed: true, Principal: by.Name, PrincipalTenant: by.Tenant}
		s.records[b] = rec
	}
	onSeal, sealErr := s.onSeal, s.sealErr
	s.mu.Unlock()
	if onSeal != nil {
		if err := onSeal(b); err != nil {
			return rec, err
		}
	}
	return rec, sealErr
}

// Clear implements clusterseal.Sealer.
func (s *Sealer) Clear(_ context.Context, b clusterseal.Barrier, by clusterseal.Principal) (clusterseal.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return clusterseal.Record{}, clusterseal.ErrInactive
	}
	s.clears = append(s.clears, b)
	s.by = append(s.by, by)
	if b.Kind() == clusterseal.KindNode && s.nodeSealed {
		return clusterseal.Record{}, seal.ErrSealed
	}
	if s.clearErr != nil {
		return clusterseal.Record{}, s.clearErr
	}
	rec := s.records[b]
	if rec.Sealed {
		rec = clusterseal.Record{Barrier: b, Epoch: rec.Epoch + 1, Principal: by.Name, PrincipalTenant: by.Tenant}
		s.records[b] = rec
	}
	return rec, nil
}

// Status implements clusterseal.Sealer.
func (s *Sealer) Status(b clusterseal.Barrier) clusterseal.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return clusterseal.Status{}
	}
	return clusterseal.Status{Active: true, Record: s.records[b], Local: s.local}
}

// Refuses implements clusterseal.Sealer.
func (s *Sealer) Refuses(b clusterseal.Barrier) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active && s.records[b].Sealed
}
