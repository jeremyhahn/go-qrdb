package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func newSealDisabledBarrierServiceForTest(t *testing.T) BarrierServicer {
	t.Helper()
	svc, err := NewSealDisabledBarrierService(context.Background())
	if err != nil {
		t.Fatalf("NewSealDisabledBarrierService: %v", err)
	}
	return svc
}

func TestSealDisabledBarrierService_StatusIsProtectionNoneUnsealed(t *testing.T) {
	svc := newSealDisabledBarrierServiceForTest(t)
	st, err := svc.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if st.Sealed || !st.Initialized || st.Strategy != string(seal.StrategyNone) || st.HardwareBacked {
		t.Fatalf("status = %+v, want strategy none, initialized, unsealed, not hardware-backed", st)
	}
	if !st.InitializedAt.IsZero() {
		t.Fatalf("InitializedAt = %v, want zero: no one initialized a seal-disabled barrier", st.InitializedAt)
	}
	if st.ClusterSeal == nil {
		t.Fatal("ClusterSeal is nil; the status contract says never nil")
	}
	if st.ClusterMigration != nil {
		t.Fatalf("ClusterMigration = %+v, want nil", st.ClusterMigration)
	}
}

func TestSealDisabledBarrierService_ReadVerbsAnswer(t *testing.T) {
	ctx := context.Background()
	svc := newSealDisabledBarrierServiceForTest(t)
	if ok, err := svc.BarrierInitialized(ctx); err != nil || !ok {
		t.Fatalf("BarrierInitialized = %v, %v; want true, nil", ok, err)
	}
	if ok, err := svc.BarrierHasRecoveryKeys(ctx); err != nil || ok {
		t.Fatalf("BarrierHasRecoveryKeys = %v, %v; want false, nil", ok, err)
	}
	holders, err := svc.BarrierOfficerList(ctx, seal.QuorumDomain("unseal"))
	if err != nil || holders == nil || len(holders) != 0 {
		t.Fatalf("BarrierOfficerList = %v, %v; want an empty list", holders, err)
	}
}

func TestSealDisabledBarrierService_RefusesEveryChangingVerb(t *testing.T) {
	ctx := context.Background()
	svc := newSealDisabledBarrierServiceForTest(t)
	domain := seal.QuorumDomain("unseal")
	var noFactorPolicy seal.FactorPolicy
	verbs := map[string]func() error{
		"initialize":        func() error { return svc.BarrierInitialize(ctx, []byte("secret")) },
		"unseal":            func() error { return svc.BarrierUnseal(ctx, []byte("secret")) },
		"change passphrase": func() error { return svc.BarrierChangePassphrase(ctx, []byte("a"), []byte("b")) },
		"seal":              func() error { return svc.BarrierSeal(ctx) },
		"cluster unseal": func() error {
			_, err := svc.BarrierClusterUnseal(ctx)
			return err
		},
		"initialize shamir": func() error {
			_, err := svc.BarrierInitializeShamir(ctx, 2, 3, noFactorPolicy)
			return err
		},
		"generate recovery keys": func() error {
			_, err := svc.BarrierGenerateRecoveryKeys(ctx, 2, noFactorPolicy)
			return err
		},
		"delete recovery keys": func() error { return svc.BarrierDeleteRecoveryKeys(ctx) },
		"officer register": func() error {
			_, err := svc.BarrierOfficerRegister(ctx, domain, &BarrierOfficerRegisterRequest{})
			return err
		},
		"share claim begin": func() error {
			_, err := svc.BarrierShareClaimBegin(ctx, domain, "u", "t")
			return err
		},
		"share claim complete": func() error {
			_, err := svc.BarrierShareClaimComplete(ctx, domain, "u", "00", nil, "t")
			return err
		},
		"quorum begin": func() error {
			_, err := svc.BarrierQuorumBegin(ctx, &QuorumBeginRequest{})
			return err
		},
		"quorum submit": func() error {
			_, err := svc.BarrierQuorumSubmit(ctx, &QuorumSubmitRequest{})
			return err
		},
		"rekey start": func() error {
			_, err := svc.BarrierRekeyStart(ctx, domain, 2, noFactorPolicy, "pin")
			return err
		},
		"officer revoke": func() error {
			_, err := svc.BarrierOfficerRevoke(ctx, domain, "u", 2, "pin")
			return err
		},
	}
	for name, call := range verbs {
		t.Run(name, func(t *testing.T) {
			var sde *SealDisabledError
			if err := call(); !errors.As(err, &sde) {
				t.Fatalf("err = %v, want *SealDisabledError", err)
			}
			if sde.Op == "" || sde.Error() == "" {
				t.Fatalf("SealDisabledError carries no verb: %+v", sde)
			}
		})
	}
	st, err := svc.BarrierStatus(ctx)
	if err != nil || st.Sealed {
		t.Fatalf("after the refused verbs status = %+v, %v; want unsealed", st, err)
	}
}
