package ceremony

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A single-admin init that fails after it unsealed a fresh barrier -- here
// at the TLS issuance -- reseals it, so the sealed window holds and startup
// cannot go on to first boot without the SO; the retry enrols the SO.
func TestInitialize_SingleAdminFaultAfterUnsealResealsAndRetryEnrolsSO(t *testing.T) {
	ctx := context.Background()
	mock := newMockCA(t)
	soCSR, _ := generateCSRPEM(t, "ignored-cn")
	opts := func() *InitializeOptions {
		return &InitializeOptions{
			SOPin:         "test-so-pin",
			UserPin:       "test-user-pin",
			AdminGenerate: true,
			SOUsername:    "secofficer",
			SOCSRPEM:      soCSR,
		}
	}
	svc := newTestCeremonyService(t, defaultConfig(), testBarrier(t))

	_, err := svc.Initialize(ctx, faultCA{CeremonyCA: mock, issueErr: errors.New("simulated TLS fault")}, opts())
	require.ErrorIs(t, err, ErrInitFailed)
	assert.True(t, svc.HoldsSealedWindow(ctx), "the failed init leaves the sealed window up")
	assert.Nil(t, svc.LastIssuedSystemSO())
	adminPEM, _ := svc.LastIssuedAdminCertPEM()
	assert.Empty(t, adminPEM)

	result, err := svc.Initialize(ctx, mock, opts())
	require.NoError(t, err, "the init is retryable")
	assert.Equal(t, StateOperational, result.State)
	assert.NotEmpty(t, result.SOCertPEM)
	pending, err := svc.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "the retry enrolled the SO")
	assert.Equal(t, "secofficer", pending.Username)
	assert.Equal(t, result.SOCertPEM, pending.CertPEM)
}
