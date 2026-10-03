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

// Package clusterseal makes a seal reach every node of a cluster.
//
// A seal is recorded as a small replicated record per barrier -- the node
// barrier, one tenant's barrier, or one database's barrier -- in the system
// shard. The record says who sealed, when, through which node, and at which
// epoch. It carries no key material. Every node runs a Monitor that reads the
// records and seals the barrier on itself, with its own key: it closes the
// gate, ends open streams, stops the barrier's shards and wipes the key.
// Unsealing stays per node; a clear record ends a cluster seal but unseals
// nothing by itself.
//
// A node serves only while it holds a fresh lease on the records: a node
// that cannot read them stops serving, keeps its keys, and resumes on its
// next successful read.
//
// See docs/barrier-seal/architecture.md, "Sealing in a cluster".
package clusterseal

import (
	"encoding/json"
	"strings"
	"time"
)

// Kind names a barrier level.
type Kind int

// The three barrier levels.
const (
	// KindInvalid is the zero Kind; no valid barrier has it.
	KindInvalid Kind = iota
	// KindNode is a node's own barrier, in front of its whole data plane.
	KindNode
	// KindTenant is one tenant's dedicated barrier.
	KindTenant
	// KindDatabase is one database's own barrier.
	KindDatabase
)

// Barrier names one barrier cluster-wide: "node", "tenant/<tenant>" or
// "database/<tenant>/<database>". Build one with NodeBarrier, TenantBarrier
// or DatabaseBarrier, or parse one with ParseBarrier.
type Barrier string

const (
	nodeName       = "node"
	tenantPrefix   = "tenant/"
	databasePrefix = "database/"
)

// NodeBarrier names the node barrier. There is one record for it, and every
// node enforces it on its own node barrier.
func NodeBarrier() Barrier { return nodeName }

// TenantBarrier names tenantID's barrier.
func TenantBarrier(tenantID string) Barrier { return Barrier(tenantPrefix + tenantID) }

// DatabaseBarrier names the barrier of database, owned by tenantID.
func DatabaseBarrier(tenantID, database string) Barrier {
	return Barrier(databasePrefix + tenantID + "/" + database)
}

// ParseBarrier parses a barrier name, refusing anything that is not exactly
// one of the three forms with non-empty, slash-free components.
func ParseBarrier(s string) (Barrier, error) {
	b := Barrier(s)
	if b.Kind() == KindInvalid {
		return "", &InvalidBarrierError{Name: s}
	}
	return b, nil
}

// Kind reports the barrier's level, or KindInvalid when the name is
// malformed.
func (b Barrier) Kind() Kind {
	s := string(b)
	switch {
	case s == nodeName:
		return KindNode
	case strings.HasPrefix(s, tenantPrefix):
		if validComponent(s[len(tenantPrefix):]) {
			return KindTenant
		}
	case strings.HasPrefix(s, databasePrefix):
		tenantID, database, ok := strings.Cut(s[len(databasePrefix):], "/")
		if ok && validComponent(tenantID) && validComponent(database) {
			return KindDatabase
		}
	}
	return KindInvalid
}

// Tenant returns the tenant a tenant or database barrier belongs to, and ""
// for the node barrier.
func (b Barrier) Tenant() string {
	switch b.Kind() {
	case KindTenant:
		return string(b)[len(tenantPrefix):]
	case KindDatabase:
		tenantID, _, _ := strings.Cut(string(b)[len(databasePrefix):], "/")
		return tenantID
	default:
		return ""
	}
}

// Database returns the database a database barrier protects, and "" for
// every other barrier.
func (b Barrier) Database() string {
	if b.Kind() != KindDatabase {
		return ""
	}
	_, database, _ := strings.Cut(string(b)[len(databasePrefix):], "/")
	return database
}

func validComponent(s string) bool {
	return s != "" && !strings.Contains(s, "/")
}

// Principal is who asked for a seal or a clear: the authenticated name and
// the tenant it belongs to.
type Principal struct {
	Name   string
	Tenant string
}

// Record is the replicated statement of one barrier's cluster seal state.
//
// It carries no key material and cannot: every field is a scalar, and a unit
// test pins the encoded field set. Never add a field that holds bytes.
type Record struct {
	// Barrier is the barrier this record is about.
	Barrier Barrier `json:"barrier"`
	// Epoch increases with every seal and every clear. Zero means the
	// barrier has never been recorded.
	Epoch uint64 `json:"epoch"`
	// Sealed is true for a seal and false for a clear.
	Sealed bool `json:"sealed"`
	// Principal and PrincipalTenant identify who wrote the record.
	Principal       string `json:"principal"`
	PrincipalTenant string `json:"principal_tenant"`
	// Node is the node that accepted the request.
	Node string `json:"node"`
	// At is when the record was written, in UTC.
	At time.Time `json:"at"`
}

// Keys in the replicated system store. "__sys:" is an owner-enumerated
// namespace, so a Scan of RecordsPrefix is not filtered.
const (
	// Prefix is the cluster-seal namespace.
	Prefix = "__sys:cluster/seal/"
	// RecordsPrefix holds one record per barrier: RecordsPrefix + barrier.
	RecordsPrefix = Prefix + "records/"
	// AcksPrefix holds each node's acknowledgement of a node-barrier seal:
	// AcksPrefix + node, valued with the acknowledged epoch in decimal.
	AcksPrefix = Prefix + "acks/"
)

func recordKey(b Barrier) string { return RecordsPrefix + string(b) }

func ackKey(node string) string { return AcksPrefix + node }

func encodeRecord(r Record) ([]byte, error) {
	return json.Marshal(r)
}

func decodeRecord(raw []byte) (Record, error) {
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return Record{}, &CorruptRecordError{Err: err}
	}
	if r.Barrier.Kind() == KindInvalid || r.Epoch == 0 {
		return Record{}, &CorruptRecordError{Barrier: r.Barrier}
	}
	return r, nil
}
