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

package embedded

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// A transport built without a family's servicer reports a wiring defect, the
// same for every family: there is no longer a mode in which a family is
// absent by design.
func TestTransport_AbsentServiceIsAWiringDefect(t *testing.T) {
	tr := newExtTransport(t)
	nilEmbed := newExtTransport(t, WithEmbeddingService(nil))
	closed := closedExtTransport(t)
	closedFactors := closedExtTransport(t, WithAuthFactorService(&mockAuthFactorService{}))
	closedCeremony := closedExtTransport(t, WithCeremonyService(&mockCeremonyService{}), WithCeremonyCA(&mockCeremonyCA{}))
	closedLease := closedExtTransport(t, WithLeaseService(&mockLeaseService{}))
	closedWatch := closedExtTransport(t, WithWatchService(&mockWatchService{}))
	ctx := t.Context()
	nilService := func(err error) bool { _, ok := errors.AsType[*NilServiceError](err); return ok }
	notImplemented := func(err error) bool { _, ok := errors.AsType[*transport.NotImplementedError](err); return ok }
	missingFactorSvc := func(err error) bool { _, ok := errors.AsType[*MissingUserAuthFactorServiceError](err); return ok }
	isClosed := func(err error) bool { _, ok := errors.AsType[*transport.ClosedError](err); return ok }

	tests := []struct {
		name string
		call func() error
		want func(error) bool
	}{
		{"CreateCollection", func() error {
			_, err := tr.CreateCollection(ctx, transport.CreateCollectionRequest{})
			return err
		}, nilService},
		{"GetCollectionInfo", func() error { _, err := tr.GetCollectionInfo(ctx, "db", "x"); return err }, nilService},
		{"ListCollections", func() error { _, err := tr.ListCollections(ctx, "db"); return err }, nilService},
		{"DropCollection", func() error { return tr.DropCollection(ctx, "db", "users") }, nilService},
		{"InsertDocument", func() error { _, err := tr.InsertDocument(ctx, transport.InsertDocumentRequest{}); return err }, nilService},
		{"GetDocument", func() error { _, err := tr.GetDocument(ctx, "db", "x", 1); return err }, nilService},
		{"UpdateDocument", func() error { return tr.UpdateDocument(ctx, transport.UpdateDocumentRequest{}) }, nilService},
		{"DeleteDocument", func() error { return tr.DeleteDocument(ctx, "db", "users", 1) }, nilService},
		{"ListDocuments", func() error { _, err := tr.ListDocuments(ctx, transport.ListDocumentsRequest{}); return err }, nilService},
		{"EnrollFactor", func() error { _, err := tr.EnrollFactor(ctx, &transport.EnrollFactorRequest{}); return err }, nilService},
		{"EnrollFactor/closed", func() error {
			_, err := closedFactors.EnrollFactor(ctx, &transport.EnrollFactorRequest{})
			return err
		}, isClosed},
		{"ChallengeFactor", func() error {
			_, err := tr.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{})
			return err
		}, nilService},
		{"ChallengeFactor/closed", func() error {
			_, err := closedFactors.ChallengeFactor(ctx, &transport.ChallengeFactorRequest{})
			return err
		}, isClosed},
		{"VerifyFactor", func() error { return tr.VerifyFactor(ctx, &transport.VerifyFactorRequest{}) }, nilService},
		{"VerifyFactor/closed", func() error { return closedFactors.VerifyFactor(ctx, &transport.VerifyFactorRequest{}) }, isClosed},
		{"ListFactors", func() error { _, err := tr.ListFactors(ctx, &transport.ListFactorsRequest{}); return err }, nilService},
		{"ListFactors/closed", func() error {
			_, err := closedFactors.ListFactors(ctx, &transport.ListFactorsRequest{})
			return err
		}, isClosed},
		{"RevokeFactor", func() error { return tr.RevokeFactor(ctx, &transport.RevokeFactorRequest{}) }, nilService},
		{"RevokeFactor/closed", func() error { return closedFactors.RevokeFactor(ctx, &transport.RevokeFactorRequest{}) }, isClosed},
		{"CeremonyInitialize", func() error {
			_, err := tr.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{})
			return err
		}, nilService},
		{"CeremonyInitialize/closed", func() error {
			_, err := closedCeremony.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{})
			return err
		}, isClosed},
		{"CeremonyStatus", func() error { _, err := tr.CeremonyStatus(ctx); return err }, nilService},
		{"CeremonyStatus/closed", func() error { _, err := closedCeremony.CeremonyStatus(ctx); return err }, isClosed},
		{"CeremonySignCSR", func() error { _, err := tr.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{}); return err }, nilService},
		{"CeremonySignCSR/closed", func() error {
			_, err := closedCeremony.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{})
			return err
		}, isClosed},
		{"LeaseGrant", func() error { _, err := tr.LeaseGrant(ctx, 5); return err }, nilService},
		{"LeaseRevoke", func() error { return tr.LeaseRevoke(ctx, 1) }, nilService},
		{"LeaseKeepAlive", func() error { _, err := tr.LeaseKeepAlive(ctx, 1); return err }, nilService},
		{"LeaseTimeToLive", func() error { _, err := tr.LeaseTimeToLive(ctx, 1); return err }, nilService},
		{"LeaseList", func() error { _, err := tr.LeaseList(ctx); return err }, nilService},
		{"LeaseGrant/closed", func() error { _, err := closedLease.LeaseGrant(ctx, 5); return err }, isClosed},
		{"LeaseRevoke/closed", func() error { return closedLease.LeaseRevoke(ctx, 1) }, isClosed},
		{"LeaseKeepAlive/closed", func() error { _, err := closedLease.LeaseKeepAlive(ctx, 1); return err }, isClosed},
		{"LeaseTimeToLive/closed", func() error { _, err := closedLease.LeaseTimeToLive(ctx, 1); return err }, isClosed},
		{"LeaseList/closed", func() error { _, err := closedLease.LeaseList(ctx); return err }, isClosed},
		{"Watch", func() error { _, err := tr.Watch(ctx, "k", transport.WatchOptions{}); return err }, nilService},
		{"WatchPrefix", func() error { _, err := tr.WatchPrefix(ctx, "p", transport.WatchOptions{}); return err }, nilService},
		{"Watch/closed", func() error { _, err := closedWatch.Watch(ctx, "k", transport.WatchOptions{}); return err }, isClosed},
		{"WatchPrefix/closed", func() error {
			_, err := closedWatch.WatchPrefix(ctx, "p", transport.WatchOptions{})
			return err
		}, isClosed},
		{"LockAcquire", func() error {
			_, err := tr.LockAcquire(ctx, &transport.LockAcquireRequest{Key: "k", HolderID: "h", TTL: 5})
			return err
		}, nilService},
		{"GetRevision", func() error { _, err := tr.GetRevision(ctx, "k"); return err }, nilService},
		{"SealDatabase", func() error { return tr.SealDatabase(ctx, "mydb") }, nilService},
		{"UnsealDatabase", func() error { return tr.UnsealDatabase(ctx, "mydb", "secret") }, nilService},
		{"DatabaseBarrierStatus", func() error { _, err := tr.DatabaseBarrierStatus(ctx, "mydb"); return err }, nilService},
		{"CredentialStore", func() error { return tr.CredentialStore(ctx, "k", []byte("v")) }, nilService},
		{"CredentialRetrieve", func() error { _, err := tr.retrieveBytes(ctx, "k"); return err }, nilService},
		{"CredentialStore/closed", func() error { return closed.CredentialStore(ctx, "k", []byte("v")) }, isClosed},
		{"Embed", func() error { _, err := tr.Embed(ctx, &transport.EmbedRequest{}); return err }, nilService},
		{"Embed/WithEmbeddingService(nil)", func() error {
			_, err := nilEmbed.Embed(ctx, &transport.EmbedRequest{})
			return err
		}, nilService},
		{"BatchEmbed", func() error { _, err := tr.BatchEmbed(ctx, &transport.BatchEmbedRequest{}); return err }, nilService},
		{"ListEmbeddingModels", func() error { _, err := tr.ListEmbeddingModels(ctx, "openai"); return err }, nilService},
		{"GetUserByCertThumbprint", func() error { _, err := tr.GetUserByCertThumbprint(ctx, "AABBCC"); return err }, notImplemented},
		{"AddCertBinding", func() error { _, err := tr.AddCertBinding(ctx, 1, "AABBCC", 0); return err }, notImplemented},
		{"RemoveCertBinding", func() error { _, err := tr.RemoveCertBinding(ctx, 1, "AABBCC"); return err }, notImplemented},
		{"LoginAuthFactor", func() error {
			_, err := tr.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{})
			return err
		}, notImplemented},
		{"AddAuthFactor", func() error { _, err := tr.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{}); return err }, missingFactorSvc},
		{"ListAuthFactors", func() error { _, err := tr.ListAuthFactors(ctx, &transport.AuthFactorListRequest{}); return err }, missingFactorSvc},
		{"GetAuthFactor", func() error { _, err := tr.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{}); return err }, missingFactorSvc},
		{"RemoveAuthFactor", func() error {
			_, err := tr.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{})
			return err
		}, missingFactorSvc},
		{"DeleteAuthFactor", func() error {
			_, err := tr.DeleteAuthFactor(ctx, &transport.AuthFactorDeleteRequest{})
			return err
		}, missingFactorSvc},
		{"RotateAuthFactor", func() error {
			_, err := tr.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{})
			return err
		}, missingFactorSvc},
		{"RelabelAuthFactor", func() error {
			_, err := tr.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{})
			return err
		}, missingFactorSvc},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !tt.want(err) {
				t.Errorf("err = %T (%v), want the absent-service error", err, err)
			}
		})
	}
}
