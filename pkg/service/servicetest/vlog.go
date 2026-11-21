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

//go:build qrdb_vlog

package servicetest

import (
	"context"
	"crypto/x509"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/transparency-dev/merkle/proof"
	"github.com/transparency-dev/merkle/rfc6962"

	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/store"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// logShardID is the one group NewLogService's logs live in.
const logShardID = 1

// logShard resolves every tenant to the one in-memory group.
type logShard struct{ host service.Host }

func (r logShard) ResolveAuditShard(context.Context, string) (service.Host, uint64, error) {
	return r.host, logShardID, nil
}

// NewLogService returns the real LogService over one in-memory state machine
// that applies the verifiable-log commands, with the software head-signing
// provider. A transport test driven against it proves the transport's mapping
// of what the service and pkg/vlog really do: keys created and rotated,
// heads signed and checkpoints recorded.
func NewLogService(t testing.TB) service.LogService {
	t.Helper()
	machine := store.NewPebbleStateMachine(&pebble.Options{FS: vfs.NewMem()})
	machine.SetLogs(vlog.NewChain())
	if _, err := machine.Open(context.Background(), "vlog", make(chan struct{})); err != nil {
		t.Fatalf("servicetest: open the log state machine: %v", err)
	}
	t.Cleanup(func() { _ = machine.Close(context.Background()) })
	databases := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = databases.Close() })
	svc, err := service.NewLogService(service.LogServiceConfig{Resolver: logShard{host: backend.NewLocalBackend(machine)}, Databases: databases})
	if err != nil {
		t.Fatalf("servicetest: NewLogService: %v", err)
	}
	return svc
}

// LogOrigin is the checkpoint origin SeedSignedLog gives its log.
const LogOrigin = "db.example.com/vlog"

// SeedSignedLog creates log in tenantID's database with an Ed25519 head key
// and one entry, through svc as the tenant's own caller, and returns the key.
func SeedSignedLog(t testing.TB, svc service.LogService, tenantID, database, log string) vlog.HeadKey {
	t.Helper()
	ctx := tenant.WithTenantID(context.Background(), tenantID)
	ref := service.LogRef{Database: database, Log: log}
	created, err := svc.CreateLog(ctx, &service.CreateLogRequest{LogRef: ref, Origin: LogOrigin, SigningKeyType: vlog.SignatureEd25519})
	if err != nil {
		t.Fatalf("servicetest: create log %s: %v", log, err)
	}
	if _, err := svc.Append(ctx, &service.AppendRequest{LogRef: ref, Entries: [][]byte{[]byte("first")}}); err != nil {
		t.Fatalf("servicetest: append to %s: %v", log, err)
	}
	return *created.Key
}

// RequireHeadSignedBy fails t unless checkpoint, a C2SP signed note, is a
// checkpoint for origin signed by key: its one signature line carries
// key's hash under the key name it names, and verifies under key's public
// key, PKIX DER.
func RequireHeadSignedBy(t testing.TB, origin string, checkpoint []byte, key vlog.HeadKey) {
	t.Helper()
	c, err := vlog.ParseCheckpoint(checkpoint)
	if err != nil {
		t.Fatalf("parse checkpoint: %v", err)
	}
	if len(c.Signatures) != 1 {
		t.Fatalf("checkpoint carries %d signatures, want 1", len(c.Signatures))
	}
	pub, err := x509.ParsePKIXPublicKey(key.PublicKey)
	if err != nil {
		t.Fatalf("key version %d: %v", key.Version, err)
	}
	if _, err := vlog.VerifyCheckpoint(checkpoint, origin, c.Signatures[0].KeyName, pub); err != nil {
		t.Fatalf("checkpoint at size %d does not verify under key version %d: %v", c.Size, key.Version, err)
	}
}

// VerifyHead checks head over origin under key's public key, PKIX DER.
func VerifyHead(origin string, head vlog.SignedHead, key vlog.HeadKey) error {
	pub, err := x509.ParsePKIXPublicKey(key.PublicKey)
	if err != nil {
		return err
	}
	return vlog.VerifyHead(pub, origin, head)
}

// VerifyInclusion checks that hashes, a log's answer to an inclusion proof
// request, proves the entry body at index is in the tree of size whose root
// is root, under RFC 9162 tree hashing.
func VerifyInclusion(body []byte, index, size uint64, hashes [][]byte, root []byte) error {
	h := rfc6962.DefaultHasher
	return proof.VerifyInclusion(h, index, size, h.HashLeaf(body), hashes, root)
}

// VerifyConsistency checks that hashes, a log's answer to a consistency proof
// request, proves the tree of size1 with root1 is a prefix of the tree of
// size2 with root2, under RFC 9162 tree hashing.
func VerifyConsistency(size1, size2 uint64, hashes [][]byte, root1, root2 []byte) error {
	return proof.VerifyConsistency(rfc6962.DefaultHasher, size1, size2, hashes, root1, root2)
}
