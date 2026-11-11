// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestCeremonyServiceWithAdminRebind_ShamirSystemSOPersistRetry drives the
// SO-persist retry through the composition qrdbd and clustered SDK nodes
// serve: the core wrapped in service.CeremonyServiceWithAdminRebind. A
// 2-of-3 ceremony whose SO record write failed restarts enrolling with no SO
// record; the retried Initialize must reach the core, which persists the SO,
// rather than the wrapper's admin reissue.
func TestCeremonyServiceWithAdminRebind_ShamirSystemSOPersistRetry(t *testing.T) {
	ctx := context.Background()
	f := ceremony.NewShamirSOPersistFault(t)
	pending, err := f.Service.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.Nil(t, pending, "the failed write left no SO record")

	composed := service.NewCeremonyServiceWithAdminRebind(service.NewCeremonyService(f.Service), f.Service, nil, nil, nil)

	wrongPIN := serviceInitOptions(f.Options())
	wrongPIN.SOPin = "not-the-so-pin"
	_, err = composed.CeremonyInitialize(ctx, f.CA, wrongPIN)
	require.Error(t, err, "the retry must prove the provisioned SO PIN")

	result, err := composed.CeremonyInitialize(ctx, f.CA, serviceInitOptions(f.Options()))
	require.NoError(t, err, "the retried ceremony persists the SO")
	require.NotNil(t, result)
	assert.Equal(t, ceremony.StateEnrolling, f.Service.State())

	pending, err = f.Reopen().PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "first boot is handed the SO")
	assert.Equal(t, "secofficer", pending.Username)
}

// serviceInitOptions carries the core options onto the service surface the
// composed ceremony serves.
func serviceInitOptions(o *ceremony.InitializeOptions) *service.CeremonyInitializeOptions {
	out := &service.CeremonyInitializeOptions{
		SOPin:            o.SOPin,
		UserPin:          o.UserPin,
		Threshold:        o.Threshold,
		Policy:           o.Policy,
		AdminCSRPEM:      string(o.AdminCSRPEM),
		AdminCertPEM:     string(o.AdminCertPEM),
		AdminCertPoolPEM: string(o.AdminCertPoolPEM),
		AdminGenerate:    o.AdminGenerate,
		SOUsername:       o.SOUsername,
		SOCSRPEM:         string(o.SOCSRPEM),
		SOCertPEM:        string(o.SOCertPEM),
		SOChainPEM:       string(o.SOChainPEM),
	}
	for _, officer := range o.Officers {
		out.Officers = append(out.Officers, service.CeremonyOfficerConfig{
			Username:      officer.Username,
			Role:          officer.Role,
			CSRPEM:        string(officer.CSRPEM),
			EncryptionKEM: officer.EncryptionKEM,
			EncryptionKey: officer.EncryptionKey,
			Factors:       officer.Factors,
			Designated:    officer.Designated,
		})
	}
	return out
}
