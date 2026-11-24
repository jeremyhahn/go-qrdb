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

// Package composer is an embedder defined outside go-qrdb that composes a node
// through the public SDK alone, the way go-xkms does. The sdk_external_composer
// gate builds and tests it as a module of its own with GOWORK=off; a seam it
// cannot reach through sdk/go is a seam the SDK does not offer.
package composer

import (
	"context"
	"crypto"
	"crypto/x509"
	"errors"
	"net/url"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	_ "google.golang.org/protobuf/types/known/emptypb" // registers google/protobuf/empty.proto for Invoice

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	qrdbv1 "github.com/jeremyhahn/go-qrdb/sdk/go/proto/qrdb/v1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// ClusterCAKey is the cluster CA key (Key A) held in the composer's custody.
func ClusterCAKey(signer crypto.Signer, cert *x509.Certificate) sdk.CAKeyProvider {
	return sdk.CAKeyAdapter{
		SignerFunc:      func(context.Context) (crypto.Signer, error) { return signer, nil },
		CertificateFunc: func(context.Context) (*x509.Certificate, error) { return cert, nil },
	}
}

// TenantKeys is a per-tenant login CA key (Key B) factory.
type TenantKeys map[string]sdk.CAKeyProvider

var _ sdk.CAKeyProviderFactory = TenantKeys(nil)

// ProviderForTenant returns tenantID's own key.
func (k TenantKeys) ProviderForTenant(tenantID string) (sdk.CAKeyProvider, error) {
	p, ok := k[tenantID]
	if !ok {
		return nil, &sdk.CAKeyNotInitializedError{}
	}
	return p, nil
}

// TenantIDs lists the tenants the factory backs.
func (k TenantKeys) TenantIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(k))
	for id := range k {
		ids = append(ids, id)
	}
	return ids, nil
}

// OwnCA is the composer's own login CA.
type OwnCA struct{ Subject string }

var _ sdk.ClientCertProvider = OwnCA{}

// IssueClientCert is not offered by this CA.
func (OwnCA) IssueClientCert(context.Context, *sdk.ClientCertIssueRequest) (*sdk.ClientCertIssued, error) {
	return nil, &sdk.CAKeyNotInitializedError{}
}

// RevokeCert records nothing.
func (OwnCA) RevokeCert(context.Context, string) (*sdk.ClientCertRevokeResult, error) {
	return &sdk.ClientCertRevokeResult{}, nil
}

// IsRevoked reports nothing revoked.
func (OwnCA) IsRevoked(context.Context, string) (bool, error) { return false, nil }

// TenantForSerial knows no serial.
func (OwnCA) TenantForSerial(context.Context, string) (string, bool, error) { return "", false, nil }

// ListCerts lists nothing.
func (OwnCA) ListCerts(context.Context, string) ([]*sdk.ClientCertRecord, error) { return nil, nil }

// VolumeKeys derives every lockable volume's key from the composer's custody.
type VolumeKeys struct{ Key sdk.VolumeKey }

var _ sdk.VolumeKeyProvider = VolumeKeys{}

// VolumeKey returns the one key this provider holds.
func (v VolumeKeys) VolumeKey(context.Context, string, string, string) (sdk.VolumeKey, error) {
	return v.Key, nil
}

// TenantKeyOptions composes a node whose built-in login CA signs with the
// composer's per-tenant keys.
func TenantKeyOptions(clusterCA sdk.CAKeyProvider, keyB sdk.CAKeyProviderFactory, volumes sdk.VolumeKeyProvider) []node.NodeOption {
	return []node.NodeOption{
		node.WithClusterCAKeyProvider(clusterCA),
		node.WithCertKeyProviderFactory(keyB),
		node.WithVolumeKeyProvider(volumes),
	}
}

// OwnCAOptions composes a node whose login CA is the composer's own.
func OwnCAOptions(clusterCA sdk.CAKeyProvider, ca sdk.ClientCertProvider, volumes sdk.VolumeKeyProvider) []node.NodeOption {
	return []node.NodeOption{
		node.WithClusterCAKeyProvider(clusterCA),
		node.WithCertProvider(ca),
		node.WithVolumeKeyProvider(volumes),
	}
}

// Ledger is the composer's entity.
type Ledger struct {
	ID   uint64 `json:"id"`
	Memo string `json:"memo"`
}

// EntityID returns the entity's ID.
func (l *Ledger) EntityID() uint64 { return l.ID }

// SetEntityID sets the entity's ID.
func (l *Ledger) SetEntityID(id uint64) { l.ID = id }

// LedgerDAOs builds the composer's DAOs over the named database.
func LedgerDAOs(f *sdk.DAOFactory, database string) (sdk.GenericDAO[*Ledger], sdk.DAOTransactional[*Ledger], error) {
	newLedger := func() *Ledger { return &Ledger{} }
	plain, err := sdk.NewDatabaseDAO(f, database, "ledger", newLedger)
	if err != nil {
		return nil, nil, err
	}
	tx, err := sdk.NewDatabaseDAOTransactional(f, database, "ledger", newLedger)
	if err != nil {
		return nil, nil, err
	}
	return plain, tx, nil
}

// TenantUserSAN builds the SPIFFE URI SAN for a tenant user: the tenant is
// the trust domain.
func TenantUserSAN(tenantID, user string) (*url.URL, error) {
	return sdk.SpiffeID{TrustDomain: tenantID, Kind: sdk.SpiffeKindUser, Name: user}.URI()
}

// DelegatedScope reads the scope a tenant delegated to principal out of the
// delegation certificate presented for it, refusing a certificate that names
// another principal or carries no delegation at all.
func DelegatedScope(c *x509.Certificate, principal string) ([]string, error) {
	g, found, err := sdk.DelegationFromCertificate(c)
	if err != nil {
		return nil, err
	}
	if !found || g.Principal != principal {
		return nil, &sdk.DelegationGrantError{Field: "principal", Reason: "the certificate delegates nothing to " + principal}
	}
	if err := sdk.DelegationCheckLifetime(c); err != nil {
		return nil, err
	}
	return g.Scope, nil
}

// DelegationOptions composes a node that accepts tenant-issued delegations
// for principal.
func DelegationOptions(principal string) []node.NodeOption {
	return []node.NodeOption{node.WithServicePrincipal(principal)}
}

// InvoicePermission links a service defined outside go-qrdb whose RPC is
// annotated with the public (qrdb.v1.permission) option -- the descriptor
// protoc emits for a file importing "qrdb/v1/permission.proto" -- and reads the
// permission back the way the authorization interceptor does.
func InvoicePermission(perm string) (string, error) {
	opts := &descriptorpb.MethodOptions{}
	proto.SetExtension(opts, qrdbv1.E_Permission, perm)
	fd, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:       proto.String("billing/v1/invoice.proto"),
		Package:    proto.String("billing.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto", "qrdb/v1/permission.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("InvoiceService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Issue"),
				InputType:  proto.String(".google.protobuf.Empty"),
				OutputType: proto.String(".google.protobuf.Empty"),
				Options:    opts,
			}},
		}},
	}, protoregistry.GlobalFiles)
	if err != nil {
		return "", err
	}
	md := fd.Services().ByName("InvoiceService").Methods().ByName("Issue")
	got, _ := proto.GetExtension(md.Options(), qrdbv1.E_Permission).(string)
	return got, nil
}

// CloseWebTransport ends a WebTransport session with an application error
// code the composer holds as a plain uint32.
func CloseWebTransport(sess *server.WebTransportSession, code uint32, msg string) error {
	return sess.CloseWithError(server.WebTransportSessionErrorCode(code), msg)
}

// WebTransportStreamErrorCode reads the application error code out of a
// stream error, and whether the peer sent it.
func WebTransportStreamErrorCode(err error) (code uint32, remote, ok bool) {
	var se *server.WebTransportStreamError
	if !errors.As(err, &se) {
		return 0, false, false
	}
	return uint32(se.ErrorCode), se.Remote, true
}

// WebTransportSessionErrorCode reads the application error code out of a
// session error.
func WebTransportSessionErrorCode(err error) (uint32, bool) {
	var se *server.WebTransportSessionError
	if !errors.As(err, &se) {
		return 0, false
	}
	return uint32(se.ErrorCode), true
}

// WebTransportStreamCancel builds the stream error code a handler passes to
// CancelRead or CancelWrite.
func WebTransportStreamCancel(code uint32) server.WebTransportStreamErrorCode {
	return server.WebTransportStreamErrorCode(code)
}
