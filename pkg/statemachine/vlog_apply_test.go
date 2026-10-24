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

package statemachine

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

var applyLog = vlog.LogID{TenantID: "acme", Database: "certs", Log: "issued"}

func applyDescriptor(expire bool) vlog.Descriptor {
	return vlog.Descriptor{Origin: "db.example.com/acme/certs", Hash: vlog.HashSHA256, VerifierKeys: []string{"key-1"}, AllowExpireBodies: expire}
}

// payloadOf returns a function that takes an encoder's two results and
// answers the command's payload -- what a state machine's dispatch hands its
// handler -- failing t on an encode error, so a call reads
// p(protocol.EncodeVerifiableLog...(...)).
func payloadOf(t *testing.T) func([]byte, error) []byte {
	return func(encoded []byte, err error) []byte {
		t.Helper()
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		cmd, err := protocol.DecodeCommand(encoded)
		if err != nil || cmd.Op != protocol.OpVerifiableLog {
			t.Fatalf("DecodeCommand = %+v, %v; want an OpVerifiableLog command", cmd, err)
		}
		return cmd.Payload
	}
}

// corruptNodes deletes every tree node record of every named log in stg.
func corruptNodes(t *testing.T, stg *memory.MemoryStorage) {
	t.Helper()
	keys, err := stg.List([]byte(vlog.LogTablePrefix))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	deleted := 0
	for _, k := range keys {
		if bytes.Contains(k, []byte("/n/")) {
			if err := stg.Delete(k); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			deleted++
		}
	}
	if deleted == 0 {
		t.Fatal("no tree node records to corrupt")
	}
}

// failingWriter refuses every write, as a storage fault would.
type failingWriter struct{}

func (failingWriter) Set(_, _ []byte) error { return errors.New("disk full") }
func (failingWriter) Delete(_ []byte) error { return errors.New("disk full") }

// TestApplyVerifiableLog_DispatchesEverySubtype applies each sub-type in turn
// and pins its result value: Create and Checkpoint answer 0, Append the
// first index it assigned, ExpireBodies the number of bodies it removed.
func TestApplyVerifiableLog_DispatchesEverySubtype(t *testing.T) {
	p := payloadOf(t)
	logs := vlog.NewChain()
	stg := memory.NewStorage()
	apply := func(payload []byte) uint64 {
		t.Helper()
		res, err := ApplyVerifiableLog(logs, stg, stg, payload)
		if err != nil {
			t.Fatalf("ApplyVerifiableLog = %v", err)
		}
		if msg, ok := protocol.DecodeAppError(res.Data); ok {
			t.Fatalf("ApplyVerifiableLog refused: %s", msg)
		}
		return res.Value
	}

	if v := apply(p(protocol.EncodeVerifiableLogCreate(applyLog, applyDescriptor(true)))); v != 0 {
		t.Fatalf("Create = %d, want 0", v)
	}
	if v := apply(p(protocol.EncodeVerifiableLogAppend(applyLog, [][]byte{[]byte("a"), []byte("b"), []byte("c")}))); v != 0 {
		t.Fatalf("first Append = %d, want first index 0", v)
	}
	if v := apply(p(protocol.EncodeVerifiableLogAppend(applyLog, [][]byte{[]byte("d")}))); v != 3 {
		t.Fatalf("second Append = %d, want first index 3", v)
	}

	head, err := vlog.ReadHead(stg, applyLog)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if head.Size != 4 {
		t.Fatalf("head size = %d, want 4", head.Size)
	}
	if v := apply(p(protocol.EncodeVerifiableLogCheckpoint(applyLog, 4, head.Root, []byte("signed-4")))); v != 0 {
		t.Fatalf("Checkpoint = %d, want 0", v)
	}
	if v := apply(p(protocol.EncodeVerifiableLogExpireBodies(applyLog, 2))); v != 2 {
		t.Fatalf("ExpireBodies = %d, want 2", v)
	}

	head, err = vlog.ReadHead(stg, applyLog)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if head.CheckpointSize != 4 || string(head.Checkpoint) != "signed-4" || head.BodiesFrom != 2 {
		t.Fatalf("head = %+v, want checkpoint 4 and bodies from 2", head)
	}
}

// TestApplyVerifiableLog_Refusals pins that every deterministic consequence of
// the committed bytes is answered as a refusal result with the kind
// RefusalKind names -- never a Go error, which would halt apply on a command
// every replica is equally unable to apply.
func TestApplyVerifiableLog_Refusals(t *testing.T) {
	p := payloadOf(t)
	create := func(t *testing.T) (*vlog.Chain, *memory.MemoryStorage) {
		t.Helper()
		logs := vlog.NewChain()
		stg := memory.NewStorage()
		if err := logs.Create(stg, applyLog, applyDescriptor(false)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := logs.AppendEntries(stg, applyLog, [][]byte{[]byte("a"), []byte("b")}); err != nil {
			t.Fatalf("AppendEntries: %v", err)
		}
		return logs, stg
	}
	missing := vlog.LogID{TenantID: applyLog.TenantID, Database: applyLog.Database, Log: "absent"}
	appendOne := p(protocol.EncodeVerifiableLogAppend(applyLog, [][]byte{[]byte("x")}))
	// The encoder refuses a tenant id with a '/', so only committed bytes
	// from elsewhere can carry one: rewrite "acme" in place.
	invalidTenant := bytes.Clone(appendOne)
	copy(invalidTenant[3:7], "ac/e")

	cases := []struct {
		name    string
		payload []byte
		want    protocol.AppErrorKind
	}{
		{"existing log", p(protocol.EncodeVerifiableLogCreate(applyLog, applyDescriptor(false))), protocol.AppErrorConflict},
		{"invalid descriptor", p(protocol.EncodeVerifiableLogCreate(missing, vlog.Descriptor{})), protocol.AppErrorInvalid},
		{"missing log", p(protocol.EncodeVerifiableLogAppend(missing, [][]byte{[]byte("x")})), protocol.AppErrorInvalid},
		{"malformed body", appendOne[:len(appendOne)-1], protocol.AppErrorInvalid},
		{"invalid tenant", invalidTenant, protocol.AppErrorInvalid},
		{"checkpoint past the head", p(protocol.EncodeVerifiableLogCheckpoint(applyLog, 9, make([]byte, 32), []byte("s"))), protocol.AppErrorInvalid},
		{"checkpoint root mismatch", p(protocol.EncodeVerifiableLogCheckpoint(applyLog, 2, make([]byte, 32), []byte("s"))), protocol.AppErrorInvalid},
		{"empty checkpoint", p(protocol.EncodeVerifiableLogCheckpoint(applyLog, 2, make([]byte, 32), nil)), protocol.AppErrorInvalid},
		{"expire not allowed", p(protocol.EncodeVerifiableLogExpireBodies(applyLog, 1)), protocol.AppErrorInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs, stg := create(t)
			res, err := ApplyVerifiableLog(logs, stg, stg, tc.payload)
			if err != nil {
				t.Fatalf("ApplyVerifiableLog = %v, want a refusal result", err)
			}
			msg, kind, ok := protocol.DecodeAppErrorKind(res.Data)
			if !ok {
				t.Fatalf("result %+v is not a refusal", res)
			}
			if kind != tc.want {
				t.Fatalf("refusal kind = %d (%s), want %d", kind, msg, tc.want)
			}
		})
	}

	t.Run("no registry", func(t *testing.T) {
		res, err := ApplyVerifiableLog(nil, memory.NewStorage(), memory.NewStorage(), appendOne)
		if err != nil {
			t.Fatalf("ApplyVerifiableLog = %v, want a refusal result", err)
		}
		if _, kind, ok := protocol.DecodeAppErrorKind(res.Data); !ok || kind != protocol.AppErrorCapability {
			t.Fatalf("result %+v, want a capability refusal", res)
		}
	})
}

// TestApplyVerifiableLog_CreateWithKey covers a Create carrying the log's
// head-signing key: the key lands in the same apply as the descriptor, and a
// key of a type RFC 9162 does not allow is refused with nothing written.
func TestApplyVerifiableLog_CreateWithKey(t *testing.T) {
	p := payloadOf(t)
	key, err := vlog.GenerateSigningKey(vlog.SignatureEd25519, vlog.FirstKeyVersion)
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	desc := applyDescriptor(false)
	desc.KeyRef = vlog.KeyRef(key.Version)

	t.Run("applied", func(t *testing.T) {
		logs, stg := vlog.NewChain(), memory.NewStorage()
		res, err := ApplyVerifiableLog(logs, stg, stg, p(protocol.EncodeVerifiableLogCreateWithKey(applyLog, desc, key)))
		if err != nil {
			t.Fatalf("ApplyVerifiableLog: %v", err)
		}
		if _, ok := protocol.DecodeAppError(res.Data); ok {
			t.Fatalf("create refused: %+v", res)
		}
		got, err := vlog.ReadSigningKey(stg, applyLog, key.Version)
		if err != nil || !bytes.Equal(got.PKCS8, key.PKCS8) {
			t.Fatalf("ReadSigningKey = %+v, %v", got, err)
		}
	})

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	rsaDER, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	t.Run("rsa refused", func(t *testing.T) {
		logs, stg := vlog.NewChain(), memory.NewStorage()
		res, err := ApplyVerifiableLog(logs, stg, stg, p(protocol.EncodeVerifiableLogCreateWithKey(applyLog, desc, vlog.SigningKey{Version: 1, PKCS8: rsaDER})))
		if err != nil {
			t.Fatalf("ApplyVerifiableLog = %v, want a refusal result", err)
		}
		if _, kind, ok := protocol.DecodeAppErrorKind(res.Data); !ok || kind != protocol.AppErrorInvalid {
			t.Fatalf("result %+v, want an invalid refusal", res)
		}
		if keys, err := stg.List([]byte(vlog.LogTablePrefix)); err != nil || len(keys) != 0 {
			t.Fatalf("a refused create left %d records (%v)", len(keys), err)
		}
	})
}

// TestApplyVerifiableLog_Halts pins the other side of the rule: a framing this
// binary does not recognise, and a fault in this replica's own storage, are
// returned as Go errors so apply halts rather than answering a refusal its
// peers did not make.
func TestApplyVerifiableLog_Halts(t *testing.T) {
	p := payloadOf(t)
	appendOne := p(protocol.EncodeVerifiableLogAppend(applyLog, [][]byte{[]byte("x")}))
	unknown := append([]byte{0xFF}, appendOne[1:]...)

	for name, payload := range map[string][]byte{
		"truncated framing": {protocol.VerifiableLogSubtypeAppend, 0},
		"unknown sub-type":  unknown,
	} {
		t.Run(name, func(t *testing.T) {
			var ice *protocol.InvalidCommandError
			if _, err := ApplyVerifiableLog(vlog.NewChain(), memory.NewStorage(), memory.NewStorage(), payload); !errors.As(err, &ice) {
				t.Fatalf("ApplyVerifiableLog = %v, want *protocol.InvalidCommandError", err)
			}
		})
	}

	t.Run("table write fault", func(t *testing.T) {
		stg := memory.NewStorage()
		var twe *vlog.TableWriteError
		_, err := ApplyVerifiableLog(vlog.NewChain(), stg, failingWriter{}, p(protocol.EncodeVerifiableLogCreate(applyLog, applyDescriptor(false))))
		if !errors.As(err, &twe) {
			t.Fatalf("ApplyVerifiableLog = %v, want *vlog.TableWriteError", err)
		}
	})

	t.Run("corrupt table", func(t *testing.T) {
		logs := vlog.NewChain()
		stg := memory.NewStorage()
		if err := logs.Create(stg, applyLog, applyDescriptor(false)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := logs.AppendEntries(stg, applyLog, [][]byte{[]byte("a"), []byte("b"), []byte("c")}); err != nil {
			t.Fatalf("AppendEntries: %v", err)
		}
		// Remove every tree node so the checkpoint's root at size 1 cannot
		// be recomputed: a fault in this replica's table, not in the bytes.
		corruptNodes(t, stg)
		logs.BeginUpdate()
		var cte *vlog.CorruptTableError
		_, err := ApplyVerifiableLog(logs, stg, stg, p(protocol.EncodeVerifiableLogCheckpoint(applyLog, 1, make([]byte, 32), []byte("s"))))
		if !errors.As(err, &cte) {
			t.Fatalf("ApplyVerifiableLog = %v, want *vlog.CorruptTableError", err)
		}
	})
}
