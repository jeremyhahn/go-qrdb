// Copyright 2026 Jeremy Hahn
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

package sotest

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SecretClient is what RunBarrierSecretTLS drives: every surface of one SDK
// transport with a verb whose request can carry a barrier secret.
type SecretClient interface {
	transport.BarrierClient
	transport.CeremonyClient
	transport.TenantClient
	transport.DatabaseClient
}

// SecretTarget is a client connected to a server that counts the requests
// reaching it. Reached excludes the requests Connect makes.
type SecretTarget struct {
	Client  SecretClient
	Reached func() int
}

// SecretCall is one secret a verb's request can carry.
type SecretCall struct {
	// Name names the verb and the secret.
	Name string
	// Kind is the secret's kind, or "" for the request's own SO PIN, which is
	// refused with *transport.SOPINRequiresTLSError.
	Kind transport.BarrierSecretKind
	// Call issues the verb, with the secret when secret is set and without
	// it otherwise.
	Call func(ctx context.Context, c SecretClient, secret bool) error
}

// secretValue is the secret every call carries. No server verifies it: the
// property under test is whether the client sends it.
const secretValue = "barrier secret value"

func secretBytes(on bool) []byte {
	if on {
		return []byte(secretValue)
	}
	return nil
}

func secretString(on bool) string {
	if on {
		return secretValue
	}
	return ""
}

// SecretCalls is every verb, and every secret its request can carry.
func SecretCalls() []SecretCall {
	return []SecretCall{
		{"BarrierInitialize init secret", transport.BarrierSecretInit, func(ctx context.Context, c SecretClient, on bool) error {
			return c.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{Secret: secretBytes(on)})
		}},
		{"BarrierInitialize user PIN", transport.BarrierSecretUserPIN, func(ctx context.Context, c SecretClient, on bool) error {
			return c.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{UserPin: secretString(on)})
		}},
		{"BarrierInitialize SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			return c.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{SOPin: secretString(on)})
		}},
		{"BarrierUnseal unseal key", transport.BarrierSecretUnsealKey, func(ctx context.Context, c SecretClient, on bool) error {
			return c.BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: secretBytes(on)})
		}},
		{"BarrierInitializeShamir SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{Threshold: 2, TotalShares: 3, SOPin: secretString(on)})
			return err
		}},
		{"BarrierInitializeShamir user PIN", transport.BarrierSecretUserPIN, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{Threshold: 2, TotalShares: 3, UserPin: secretString(on)})
			return err
		}},
		{"BarrierEnableEncryption passphrase", transport.BarrierSecretPassphrase, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{TenantID: "acme", Strategy: "passphrase", Passphrase: secretBytes(on)})
			return err
		}},
		{"BarrierGenerateRecoveryKeys SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, SOPin: secretString(on)})
			return err
		}},
		{"BarrierQuorumSubmit share", transport.BarrierSecretShare, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{Domain: transport.DomainUnseal, Op: "unseal", Username: "officer", Share: secretBytes(on)})
			return err
		}},
		{"BarrierRekeyStart SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: secretString(on)})
			return err
		}},
		{"BarrierOfficerRevoke SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: transport.DomainUnseal, Username: "officer", Threshold: 2, SOPin: secretString(on)})
			return err
		}},
		{"CeremonyInitialize SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{SOPin: secretString(on)})
			return err
		}},
		{"CeremonyInitialize user PIN", transport.BarrierSecretUserPIN, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{UserPin: secretString(on)})
			return err
		}},
		{"CeremonyInitialize init secret", transport.BarrierSecretInit, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.CeremonyInitialize(ctx, &transport.CeremonyInitializeRequest{Secret: secretBytes(on)})
			return err
		}},
		{"CeremonySignCSR SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{Username: "officer", CSRPEM: "csr", Role: "officer", SOPin: secretString(on)})
			return err
		}},
		{"TenantBarrierInit passphrase", transport.BarrierSecretPassphrase, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.TenantBarrierInit(ctx, &transport.TenantBarrierInitRequest{TenantID: "acme", Strategy: string(seal.StrategyShamir), Threshold: 2, Shares: 3, Passphrase: secretBytes(on)})
			return err
		}},
		{"TenantBarrierUnseal unseal key", transport.BarrierSecretUnsealKey, func(ctx context.Context, c SecretClient, on bool) error {
			return c.TenantBarrierUnseal(ctx, &transport.TenantBarrierUnsealRequest{TenantID: "acme", Key: secretBytes(on)})
		}},
		{"TenantBarrierQuorumSubmit share", transport.BarrierSecretShare, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{TenantID: "acme", Op: "unseal", Username: "officer", Share: secretBytes(on)})
			return err
		}},
		{"CreateTenant SO PIN", "", func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.CreateTenant(ctx, &transport.CreateTenantRequest{ID: "acme", Name: "acme", AdminUsername: "admin", SOPin: secretString(on)})
			return err
		}},
		{"CreateDatabase barrier passphrase", transport.BarrierSecretPassphrase, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.CreateDatabase(ctx, transport.CreateDatabaseRequest{Name: "db1", Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Secret: secretString(on)}})
			return err
		}},
		{"UpdateDatabaseBarrier passphrase", transport.BarrierSecretPassphrase, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.UpdateDatabaseBarrier(ctx, transport.UpdateDatabaseBarrierRequest{Name: "db1", Barrier: &transport.DatabaseBarrierConfig{Enabled: true, Secret: secretString(on)}})
			return err
		}},
		{"UnsealDatabase unseal key", transport.BarrierSecretUnsealKey, func(ctx context.Context, c SecretClient, on bool) error {
			return c.UnsealDatabase(ctx, "db1", secretString(on))
		}},
		{"DatabaseBarrierQuorumSubmit share", transport.BarrierSecretShare, func(ctx context.Context, c SecretClient, on bool) error {
			_, err := c.DatabaseBarrierQuorumSubmit(ctx, &transport.DatabaseQuorumSubmitRequest{Database: "db1", QuorumSubmitRequest: transport.QuorumSubmitRequest{Op: "unseal", Username: "officer", Share: secretBytes(on)}})
			return err
		}},
	}
}

// RunBarrierSecretTLS asserts, for every SecretCalls verb, that transportName's
// client refuses to send the secret over plaintext -- with the typed error,
// before any request reaches the server -- and sends it over TLS, and that the
// same verb without the secret is still sent over plaintext.
func RunBarrierSecretTLS(t *testing.T, transportName string, plaintext, overTLS SecretTarget) {
	for _, call := range SecretCalls() {
		t.Run(call.Name, func(t *testing.T) {
			t.Run("refused over plaintext", func(t *testing.T) {
				before := plaintext.Reached()
				err := issue(call, plaintext.Client, true)
				if !refusedAs(err, transportName, call.Kind) {
					t.Fatalf("got %s, want the %s transport's TLS refusal for the %s", describe(err), transportName, kindName(call.Kind))
				}
				if n := plaintext.Reached() - before; n != 0 {
					t.Fatalf("a refused request reached the server %d times", n)
				}
			})
			t.Run("sent over TLS", func(t *testing.T) {
				before := overTLS.Reached()
				if err := issue(call, overTLS.Client, true); isTLSRefusal(err) {
					t.Fatalf("refused over TLS: %s", describe(err))
				}
				if overTLS.Reached() == before {
					t.Fatal("the request never reached the server over TLS")
				}
			})
			t.Run("sent over plaintext without the secret", func(t *testing.T) {
				before := plaintext.Reached()
				if err := issue(call, plaintext.Client, false); isTLSRefusal(err) {
					t.Fatalf("refused without a secret: %s", describe(err))
				}
				if plaintext.Reached() == before {
					t.Fatal("the request without a secret never reached the server")
				}
			})
		})
	}
}

func issue(call SecretCall, c SecretClient, secret bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), RequestTimeout)
	defer cancel()
	return call.Call(ctx, c, secret)
}

// refusedAs reports whether err is transportName's TLS refusal for kind.
func refusedAs(err error, transportName string, kind transport.BarrierSecretKind) bool {
	if kind == "" {
		var pinErr *transport.SOPINRequiresTLSError
		return errors.As(err, &pinErr) && pinErr.Transport == transportName
	}
	var secretErr *transport.BarrierSecretRequiresTLSError
	return errors.As(err, &secretErr) && secretErr.Transport == transportName && secretErr.Kind == kind
}

func isTLSRefusal(err error) bool {
	var pinErr *transport.SOPINRequiresTLSError
	var secretErr *transport.BarrierSecretRequiresTLSError
	return errors.As(err, &pinErr) || errors.As(err, &secretErr)
}

func kindName(kind transport.BarrierSecretKind) string {
	if kind == "" {
		return "SO PIN"
	}
	return string(kind)
}
