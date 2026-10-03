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

package main

import "github.com/jeremyhahn/go-qrdb/sdk/go/transport"

// EmbeddedConfig stores configuration for the embedded server.
type EmbeddedConfig struct {
	StorageBackend string      `json:"storageBackend"`
	StoreEngine    string      `json:"storeEngine"`
	DataDir        string      `json:"dataDir"`
	RaftEnabled    bool        `json:"raftEnabled"`
	RaftConfig     *RaftConfig `json:"raftConfig,omitempty"`
	SealConfig     *SealConfig `json:"sealConfig,omitempty"`
	// Audit is the embedded server's compliance audit trail. Off unless
	// enabled.
	Audit *EmbeddedAuditConfig `json:"audit,omitempty"`
}

// EmbeddedAuditConfig configures the embedded server's compliance audit
// trail: the same hash-chained file, ring and operational log qrdbd keeps
// (audit.OpenTrail). Off by default. When enabled the trail is opened before
// the seal barrier is built -- so the barrier's and the share holders' events
// from the first sealed moment are recorded -- and a trail that cannot be
// opened stops the server from starting.
type EmbeddedAuditConfig struct {
	// Enabled turns the trail on.
	Enabled bool `json:"enabled"`
	// Path is the trail file, relative to the data directory; empty is
	// qrdbd's default, audit.DefaultTrailFile. It may not leave the data
	// directory.
	Path string `json:"path,omitempty"`
}

// RaftConfig stores Raft cluster configuration for the embedded server.
type RaftConfig struct {
	SingleNode      bool     `json:"singleNode"`
	NodeCount       int      `json:"nodeCount"`
	ShardID         uint64   `json:"shardID"`
	ReplicaID       uint64   `json:"replicaID"`
	RaftAddress     string   `json:"raftAddress"`
	Peers           []string `json:"peers"`
	Discovery       string   `json:"discovery"`
	MulticastSecret string   `json:"multicastSecret,omitempty"`
	DNSDomain       string   `json:"dnsDomain,omitempty"`
}

// SealConfig stores seal configuration for the embedded server.
type SealConfig struct {
	Mode           string `json:"mode"`
	SharesRequired int    `json:"sharesRequired,omitempty"`
	SharesTotal    int    `json:"sharesTotal,omitempty"`
}

// SealStatus represents the current seal state of the server.
type SealStatus struct {
	Sealed         bool   `json:"sealed"`
	Initialized    bool   `json:"initialized"`
	Strategy       string `json:"strategy"`
	SharesRequired int    `json:"sharesRequired"`
	SharesProvided int    `json:"sharesProvided"`
	SharesTotal    int    `json:"sharesTotal"`
	// ClusterSeal is the node barrier's cluster seal state: whether cluster
	// sealing is active, the record, and this node's own state -- held, cut
	// off or serving. Nil from a server that does not report it.
	ClusterSeal *transport.ClusterSeal `json:"clusterSeal"`
}

// BootstrapResult contains the share-free result of a barrier
// initialization. Share material is never returned; Shamir shares are
// sealed to enrolled security officers and released only through the
// claim ceremony. RootToken is empty for Shamir initialization, where
// root-token generation requires a post-claim share quorum.
type BootstrapResult struct {
	RootToken  string `json:"rootToken"`
	Threshold  int    `json:"threshold"`
	ShareCount int    `json:"shareCount"`
}

// UnsealProgress contains the result of providing a single unseal
// share, indicating how many shares have been received so far.
type UnsealProgress struct {
	Sealed         bool `json:"sealed"`
	SharesProvided int  `json:"sharesProvided"`
	SharesRequired int  `json:"sharesRequired"`
}

// EnableEncryptionResult acknowledges that an enable-encryption migration has
// started on a previously-unencrypted barrier. The migration runs
// asynchronously; the GUI polls the seal status until the strategy changes from
// "none" to the keyed strategy to observe completion.
type EnableEncryptionResult struct {
	Status string `json:"status"`
	Scope  string `json:"scope"`
}

// ConnectionConfig stores connection configuration for a remote server.
type ConnectionConfig struct {
	Transport string `json:"transport"`
	Address   string `json:"address"`
	CAFile    string `json:"caFile,omitempty"`
	CertFile  string `json:"certFile,omitempty"`
	KeyFile   string `json:"keyFile,omitempty"`
}
