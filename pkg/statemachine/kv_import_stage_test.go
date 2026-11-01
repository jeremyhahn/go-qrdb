package statemachine

import (
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

func mustImportStage(t *testing.T, cmd protocol.ImportStageCommand) []byte {
	t.Helper()
	b, err := protocol.EncodeImportStage(cmd)
	if err != nil {
		t.Fatalf("EncodeImportStage: %v", err)
	}
	return b
}

// anyLeaseBackend returns one of leaseBackends, for a test the backend does
// not decide.
func anyLeaseBackend() Backend {
	for _, nb := range leaseBackends() {
		return nb()
	}
	return nil
}

func lookupBytes(t *testing.T, s *leaseSM, q interface{}) []byte {
	t.Helper()
	v, err := s.k.Lookup(q)
	if err != nil {
		t.Fatalf("Lookup(%T): %v", q, err)
	}
	b, _ := v.([]byte)
	return b
}

func TestKVImportStage_MetaAndChunkCommitUnderTheLease(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb())
			id := s.grant(60)

			res := s.apply(mustImportStage(t, protocol.ImportStageCommand{
				Kind: protocol.ImportStageSetMeta, SessionID: "s1", LeaseID: id,
				TenantID: "t1", MaxSessions: 2, Body: []byte(`{"v":1}`),
			}))
			if res.Value != 1 || len(res.Data) != 0 {
				t.Fatalf("SetMeta = %+v; want Value 1 and no refusal", res)
			}
			res = s.apply(mustImportStage(t, protocol.ImportStageCommand{
				Kind: protocol.ImportStageSetChunk, SessionID: "s1", LeaseID: id,
				Seq: 3, Body: []byte("chunk"), MetaBody: []byte(`{"v":2}`),
			}))
			if res.Value != 1 {
				t.Fatalf("SetChunk = %+v; want Value 1", res)
			}

			if got := string(lookupBytes(t, s, ImportStageChunkQuery{SessionID: "s1", Seq: 3})); got != "chunk" {
				t.Fatalf("chunk = %q; want %q", got, "chunk")
			}
			if got := string(lookupBytes(t, s, ImportStageMetaQuery{SessionID: "s1"})); got != `{"v":2}` {
				t.Fatalf("meta = %q; want the chunk's MetaBody, committed with it", got)
			}
			n, err := s.k.Lookup(ImportStageOpenSessionsQuery{TenantID: "t1"})
			if err != nil || n.(int) != 1 {
				t.Fatalf("open sessions = %v, %v; want 1", n, err)
			}

			// Revoking the lease reclaims every staged record, the tenant
			// index entry included, so the session no longer counts.
			s.revoke(id)
			if got := lookupBytes(t, s, ImportStageChunkQuery{SessionID: "s1", Seq: 3}); got != nil {
				t.Fatalf("chunk survived its lease: %q", got)
			}
			n, err = s.k.Lookup(ImportStageOpenSessionsQuery{TenantID: "t1"})
			if err != nil || n.(int) != 0 {
				t.Fatalf("open sessions after revoke = %v, %v; want 0", n, err)
			}
		})
	}
}

func TestKVImportStage_SessionQuotaIsCountedAtApply(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb())
			id := s.grant(60)
			meta := func(session, tenant string) sm.Result {
				return s.apply(mustImportStage(t, protocol.ImportStageCommand{
					Kind: protocol.ImportStageSetMeta, SessionID: session, LeaseID: id,
					TenantID: tenant, MaxSessions: 1, Body: []byte("{}"),
				}))
			}
			if res := meta("s1", "t1"); res.Value != 1 {
				t.Fatalf("first session = %+v; want admitted", res)
			}
			res := meta("s2", "t1")
			if res.Value != 0 || len(res.Data) == 0 {
				t.Fatalf("second session = %+v; want the quota refusal", res)
			}
			if got := lookupBytes(t, s, ImportStageMetaQuery{SessionID: "s2"}); got != nil {
				t.Fatalf("a refused session wrote its metadata: %q", got)
			}
			// The quota is per tenant.
			if res := meta("s3", "t2"); res.Value != 1 {
				t.Fatalf("other tenant's session = %+v; want admitted", res)
			}
		})
	}
}

func TestKVImportStage_RefusesAMissingLease(t *testing.T) {
	for name, nb := range leaseBackends() {
		t.Run(name, func(t *testing.T) {
			s := newLeaseSM(t, nb())
			for _, id := range []int64{lease.NoLease, 999} {
				res := s.apply(mustImportStage(t, protocol.ImportStageCommand{
					Kind: protocol.ImportStageSetChunk, SessionID: "s1", LeaseID: id, Body: []byte("x"),
				}))
				if res.Value != 0 || len(res.Data) == 0 {
					t.Fatalf("lease %d: result = %+v; want a refusal", id, res)
				}
			}
			if got := lookupBytes(t, s, ImportStageChunkQuery{SessionID: "s1"}); got != nil {
				t.Fatalf("a refused chunk was written: %q", got)
			}
		})
	}
}

func TestKVImportStage_UndecodablePayloadIsAnError(t *testing.T) {
	s := newLeaseSM(t, anyLeaseBackend())
	results := make([]sm.Result, 1)
	if err := s.k.Update([]sm.Entry{{Index: 1, Cmd: []byte{protocol.OpImportStage}}}, results); err == nil && len(results[0].Data) == 0 {
		t.Fatalf("truncated import stage = %+v; want an error", results[0])
	}
}

func TestKVImportStage_LookupIgnoresOtherQueries(t *testing.T) {
	s := newLeaseSM(t, anyLeaseBackend())
	v, handled, err := s.k.lookupImportStage(struct{}{})
	if handled || v != nil || err != nil {
		t.Fatalf("lookupImportStage(other) = %v, %v, %v; want unhandled", v, handled, err)
	}
}
