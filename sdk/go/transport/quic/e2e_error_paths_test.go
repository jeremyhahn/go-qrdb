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

package quic

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// garbageProto is a payload that fails protobuf unmarshalling. The first
// byte declares a field of type "fixed64" but only one byte of data
// follows — every protobuf parser rejects this as a truncated frame.
// Note: a totally empty byte slice unmarshals as a zero-value message in
// protobuf so it cannot be used to exercise the error path.
var garbageProto = []byte{0xff, 0xff, 0xff}

// garbageJSON is a payload that fails JSON unmarshalling.
var garbageJSON = []byte("not-json")

// ---------------------------------------------------------------------------
// Unmarshal error paths for the proto methods
// ---------------------------------------------------------------------------

func TestE2E_Get_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.Get(context.Background(), "k")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_List_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.List(context.Background(), "p")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_Scan_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Scan", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.Scan(context.Background(), "p")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_Health_UnmarshalError(t *testing.T) {
	// Note: cannot use startE2EServer here because its default Health
	// responder is needed for Connect(). Override the Health responder
	// after the harness starts.
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Health", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.Health(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_Status_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Status", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.Status(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_Peers_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Peers", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.Peers(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_Capabilities_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Capabilities", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.Capabilities(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetShardState_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetShardState", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.GetShardState(context.Background(), 1)
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetTopology_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetTopology", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.GetTopology(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

// ---------------------------------------------------------------------------
// Server-error paths (statusCode != OK from server)
// ---------------------------------------------------------------------------

func TestE2E_Put_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("kv.Put", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("disk full")
	})
	if err := tr.Put(context.Background(), "k", []byte("v")); err == nil {
		t.Fatal("expected server error")
	}
}

func TestE2E_AddMember_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.AddMember", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("bad config")
	})
	if err := tr.AddMember(context.Background(), 1, 2, "a:1", ""); err == nil {
		t.Fatal("expected server error")
	}
}

func TestE2E_Drain_ServerError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.Drain", func(_ []byte) (uint16, []byte) {
		return wire.StatusError, []byte("not leader")
	})
	if err := tr.Drain(context.Background()); err == nil {
		t.Fatal("expected server error")
	}
}

// ---------------------------------------------------------------------------
// JSON unmarshal error paths
// ---------------------------------------------------------------------------

func TestE2E_GetDatabase_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.GetDatabase(context.Background(), "db")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListDatabases_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("db.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.ListDatabases(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetUser_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("user.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.GetUser(context.Background(), 1)
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetRole_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.GetRole(context.Background(), "r1")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListPermissions_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("role.ListPermissions", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.ListPermissions(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_IssueCert_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Issue", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.IssueCert(context.Background(), &transport.IssueCertRequest{Username: "alice", CSRPEM: "csr"})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_EnrollCert_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.Enroll", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.EnrollCert(context.Background(), &transport.EnrollCertRequest{Username: "alice", CertPEM: "pem"})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListCerts_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("cert.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.ListCerts(context.Background(), "t1")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_EnrollFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Enroll", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.EnrollFactor(context.Background(), &transport.EnrollFactorRequest{
		FactorName: "yubikey", Subject: "alice",
	})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ChallengeFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.Challenge", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.ChallengeFactor(context.Background(), &transport.ChallengeFactorRequest{EnrollmentID: "e1"})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListFactors_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("authfactor.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.ListFactors(context.Background(), &transport.ListFactorsRequest{})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_AddAuthFactor_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.Add", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.AddAuthFactor(context.Background(), &transport.AuthFactorAddRequest{UserID: 1, Type: "mtls"})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_ListAuthFactors_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("userauthfactor.List", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.ListAuthFactors(context.Background(), &transport.AuthFactorListRequest{UserID: 1})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_TSRichQuery_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.RichQuery", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.TSRichQuery(context.Background(), &transport.TSRichQueryRequest{Metric: "m"})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_TSGetRetention_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("ts.GetRetention", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{Metric: "m"})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_TenantBarrierStatus_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("tenant.BarrierStatus", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageJSON
	})
	_, err := tr.TenantBarrierStatus(context.Background(), &transport.TenantBarrierStatusRequest{TenantID: "t1"})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetCollectionInfo_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.GetInfo", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.GetCollectionInfo(context.Background(), "db", "c")
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_GetDocument_UnmarshalDocError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("collection.GetDocument", func(_ []byte) (uint16, []byte) {
		// The proto unmarshal succeeds but the Document field is not
		// valid JSON, triggering the json.Unmarshal failure path.
		return wire.StatusOK, mustMarshal(stubDocResponse("not-json", 7))
	})
	_, err := tr.GetDocument(context.Background(), "db", "c", 7)
	if err == nil {
		t.Fatal("expected JSON document unmarshal error")
	}
}

func TestE2E_GetVector_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.Get", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.GetVector(context.Background(), &transport.GetVectorRequest{Namespace: "ns", ID: 1})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestE2E_SearchSimilar_UnmarshalError(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("vector.SearchSimilar", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, garbageProto
	})
	_, err := tr.SearchSimilar(context.Background(), &transport.SearchSimilarRequest{
		Namespace: "ns", QueryVector: []float32{0.1}, K: 1,
	})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}
