// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ceremony

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// TestRegister_RefusesRSAHolderKeys: a holder's certificate key must be ECDSA
// P-256, P-384 or P-521, or Ed25519. RSA is refused at enrolment with
// *UnsupportedHolderKeyError: no decoy can be derived per name for an RSA key
// at a cost that does not reveal it, so an RSA holder would be told apart
// from a stranger by the key type alone.
func TestRegister_RefusesRSAHolderKeys(t *testing.T) {
	e := newHolderEnv(t, 2, 2)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	_, err = e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, newHolderWithKey(t, "alice", "", key).request("admin"))
	var unsupported *UnsupportedHolderKeyError
	require.ErrorAs(t, err, &unsupported)
	require.Equal(t, "RSA", unsupported.Algorithm)

	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, err = e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, newHolderWithKey(t, "bob", "", edKey).withEncryptionKey(t, testEnvelopeKEM(keydelivery.KEMX25519)).request("admin"))
	require.NoError(t, err, "Ed25519 is enrolled")
	_, err = e.svc.RegisterOfficer(context.Background(), seal.DomainUnseal, newP384Holder(t, "carol", "").request("admin"))
	require.NoError(t, err, "P-384 is enrolled")
}
