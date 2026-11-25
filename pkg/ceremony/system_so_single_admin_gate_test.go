package ceremony

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// gateAdmitsCeremonyInitialize is the sealed gate's decision for an
// anonymous ceremony Initialize on the sealed-window listener, the call a
// single-admin retry arrives as.
func gateAdmitsCeremonyInitialize(ctx context.Context, svc *CeremonyService) bool {
	return sealedwindow.Decide(sealedwindow.Request{
		Sealed:             true,
		Allowlisted:        true,
		PreOperationalOnly: sealedwindow.PreOperationalOnly(sealedwindow.VerbCeremonyInitialize),
		Listener:           true,
		EnrollmentOpen:     svc.EnrollmentOpen(ctx),
	}) == sealedwindow.Serve
}

// A single-admin Initialize whose system SO record write failed leaves the
// barrier initialized and sealed again. The sealed gate must still admit
// the ceremony Initialize retry, on the same listener and after a restart,
// and nothing else: anonymous officer enrollment stays refused.
func TestSealedGate_AdmitsSingleAdminSOWriteRetryOnly(t *testing.T) {
	ctx := context.Background()
	anon := sealedwindow.WithAnonymous(ctx)

	for _, restart := range []bool{false, true} {
		name := "same listener"
		if restart {
			name = "after restart"
		}
		t.Run(name, func(t *testing.T) {
			e := newSingleAdminSOFaultEnv(t)
			b, svc := e.boot()
			require.True(t, gateAdmitsCeremonyInitialize(ctx, svc), "a fresh node admits init")

			_, err := svc.Initialize(ctx, e.ca, e.opts())
			require.ErrorIs(t, err, errSystemSOWrite)
			if restart {
				b, svc = e.boot()
			}
			requireRecoverable(t, b, svc)

			assert.True(t, gateAdmitsCeremonyInitialize(ctx, svc),
				"the sealed gate must admit the retry of the failed SO write")
			_, err = svc.RegisterOfficer(anon, seal.DomainUnseal, RegisterOfficerRequest{Username: "late-officer"})
			assert.ErrorIs(t, err, ErrEnrollmentClosed, "the retry state admits no officer enrollment")

			result, err := svc.Initialize(ctx, e.ca, e.opts())
			require.NoError(t, err, "the admitted retry completes the ceremony")
			requireSOHandedOff(t, b, svc, result)
			assert.False(t, svc.EnrollmentOpen(ctx), "an operational node closes the gate")
		})
	}
}
