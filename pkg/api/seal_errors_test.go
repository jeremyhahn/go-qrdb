//go:build !integration

package api

import (
	"errors"
	"testing"

	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
)

func TestSealSimpleErrors(t *testing.T) {
	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.DragonError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{"TPMUnavailable", func() error { return &TPMUnavailableError{} }, func() *dberrors.DragonError { return NewTPMUnavailableError("seal.init") }, "TPM hardware not available", dberrors.ErrUnavailable},
		{"PCRMismatch", func() error { return &PCRMismatchError{} }, func() *dberrors.DragonError { return NewPCRMismatchError("seal.unseal") }, "PCR values do not match sealed policy", dberrors.ErrPreconditionFailed},
		{"TPMSealModeRequired", func() error { return &TPMSealModeRequiredError{} }, func() *dberrors.DragonError { return NewTPMSealModeRequiredError("seal.unseal") }, "data was sealed with TPM but TPM is not available", dberrors.ErrUnavailable},
		{"DuplicateShareIndex", func() error { return &DuplicateShareIndexError{} }, func() *dberrors.DragonError { return NewDuplicateShareIndexError("seal.combine") }, "duplicate share index detected", dberrors.ErrInvalidInput},
		{"MasterKeyUnavailable", func() error { return &MasterKeyUnavailableError{} }, func() *dberrors.DragonError { return NewMasterKeyUnavailableError("seal.get") }, "master key not available", dberrors.ErrInternal},
		{"KeyMismatch", func() error { return &KeyMismatchError{} }, func() *dberrors.DragonError { return NewKeyMismatchError("seal.unseal") }, "provided shares do not reconstruct the correct master key", dberrors.ErrUnauthorized},
		{"BootstrapRequired", func() error { return &BootstrapRequiredError{} }, func() *dberrors.DragonError { return NewBootstrapRequiredError("seal.status") }, "seal manager not initialized - bootstrap required", dberrors.ErrInvalidInput},
		{"NoLocalShare", func() error { return &NoLocalShareError{} }, func() *dberrors.DragonError { return NewNoLocalShareError("seal.share") }, "no local share available", dberrors.ErrInternal},
		{"CoordinatorAlreadyStarted", func() error { return &CoordinatorAlreadyStartedError{} }, func() *dberrors.DragonError { return NewCoordinatorAlreadyStartedError("coord.start") }, "coordinator already started", dberrors.ErrInternal},
		{"EmptySecret", func() error { return &EmptySecretError{} }, func() *dberrors.DragonError { return NewEmptySecretError("seal.split") }, "secret cannot be empty", dberrors.ErrInvalidInput},
		{"NoSharesProvided", func() error { return &NoSharesProvidedError{} }, func() *dberrors.DragonError { return NewNoSharesProvidedError("seal.combine") }, "no shares provided", dberrors.ErrInvalidInput},
		{"SharesEmpty", func() error { return &SharesEmptyError{} }, func() *dberrors.DragonError { return NewSharesEmptyError("seal.combine") }, "shares cannot be empty", dberrors.ErrInvalidInput},
		{"TPMDeviceClosed", func() error { return &TPMDeviceClosedError{} }, func() *dberrors.DragonError { return NewTPMDeviceClosedError("tpm.seal") }, "TPM device is closed", dberrors.ErrUnavailable},
		{"SimulatedWrite", func() error { return &SimulatedWriteError{} }, func() *dberrors.DragonError { return NewSimulatedWriteError("test") }, "simulated write error", dberrors.ErrInternal},
		{"SimulatedRead", func() error { return &SimulatedReadError{} }, func() *dberrors.DragonError { return NewSimulatedReadError("test") }, "simulated read error", dberrors.ErrInternal},
		{"CacheMiss", func() error { return &CacheMissError{} }, func() *dberrors.DragonError { return NewCacheMissError("cache.get") }, "cache miss", dberrors.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}

func TestSealFieldErrors(t *testing.T) {
	tests := []struct {
		name    string
		errFn   func() error
		newFn   func() *dberrors.DragonError
		wantMsg string
	}{
		{"AlreadyInitialized", func() error { return &AlreadyInitializedError{Component: "seal"} }, func() *dberrors.DragonError { return NewAlreadyInitializedError("seal.init", "seal") }, "seal already initialized"},
		{"UnsealRequired", func() error { return &UnsealRequiredError{Operation: "read data"} }, func() *dberrors.DragonError { return NewUnsealRequiredError("svc.read", "read data") }, "database must be unsealed to read data"},
		{"FeatureNotEnabled", func() error { return &FeatureNotEnabledError{Feature: "auto-unseal"} }, func() *dberrors.DragonError { return NewFeatureNotEnabledError("seal.auto", "auto-unseal") }, "auto-unseal not enabled"},
		{"ChannelFull", func() error { return &ChannelFullError{Channel: "share"} }, func() *dberrors.DragonError { return NewChannelFullError("coord", "share") }, "share channel full"},
		{"InvalidShare", func() error { return &InvalidShareError{Reason: "index out of range"} }, func() *dberrors.DragonError { return NewInvalidShareError("seal.submit", "index out of range") }, "index out of range"},
		{"ConfigValidation", func() error { return &ConfigValidationError{Message: "threshold > shares"} }, func() *dberrors.DragonError { return NewConfigValidationError("seal.init", "threshold > shares") }, "threshold > shares"},
		{"InvalidSealMode", func() error { return &InvalidSealModeError{Mode: "custom"} }, func() *dberrors.DragonError { return NewInvalidSealModeError("seal.init", "custom") }, "invalid seal mode: custom"},
		{"SealModeOnly", func() error { return &SealModeOnlyError{RequiredMode: "shamir"} }, func() *dberrors.DragonError { return NewSealModeOnlyError("seal.op", "shamir") }, "operation only available in shamir mode"},
		{"TPMCommandFailed", func() error { return &TPMCommandFailedError{Command: "CreatePrimary"} }, func() *dberrors.DragonError { return NewTPMCommandFailedError("tpm", "CreatePrimary") }, "CreatePrimary failed"},
		{"TPMEmptyData", func() error { return &TPMEmptyDataError{Field: "plaintext"} }, func() *dberrors.DragonError { return NewTPMEmptyDataError("tpm.seal", "plaintext") }, "plaintext cannot be empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			de := tt.newFn()
			if de.Err.Error() != tt.wantMsg {
				t.Errorf("constructor msg = %q, want %q", de.Err.Error(), tt.wantMsg)
			}
		})
	}
}

func TestInsufficientSharesError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InsufficientSharesError{Required: 3, Provided: 1}
		want := "need at least 3 shares, got 1"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewInsufficientSharesError("seal.unseal", 5, 2)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestShareTimeoutError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &ShareTimeoutError{Have: 2, Need: 5}
		want := "timeout waiting for shares: have 2, need 5"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewShareTimeoutError("coord.collect", 1, 3)
		if de.Code != dberrors.ErrTimeout {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestInconsistentShareLengthError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InconsistentShareLengthError{Index: 4}
		want := "share 4 has inconsistent length"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewInconsistentShareLengthError("seal.combine", 2)
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestInvalidStrategyError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &InvalidStrategyError{Name: "distribution", Strategy: "random", Allowed: []string{"round-robin", "consistent-hash"}}
		want := "invalid distribution: random (allowed: [round-robin consistent-hash])"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewInvalidStrategyError("seal.init", "mode", "custom", []string{"shamir", "tpm"})
		if de.Code != dberrors.ErrInvalidInput {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestTPMHandleErrors(t *testing.T) {
	t.Run("HandleNotFound", func(t *testing.T) {
		e := &TPMHandleNotFoundError{Handle: 0x81000001}
		want := "object not found at handle 0x81000001"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewTPMHandleNotFoundError("tpm.load", 0x81000001)
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
	t.Run("SessionNotFound", func(t *testing.T) {
		e := &TPMSessionNotFoundError{Handle: 0x03000000}
		want := "session not found at handle 0x3000000"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		de := NewTPMSessionNotFoundError("tpm.session", 0x03000000)
		if de.Code != dberrors.ErrNotFound {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestNoSealedKeyDataError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &NoSealedKeyDataError{}
		if got := e.Error(); got != "no sealed key data" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewNoSealedKeyDataError("seal.unseal")
		if de.Code != dberrors.ErrInternal {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestAutoUnsealRequirementsError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &AutoUnsealRequirementsError{}
		if got := e.Error(); got != "TPM and share stores are required for auto unseal" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewAutoUnsealRequirementsError("seal.auto")
		if de.Code != dberrors.ErrPreconditionFailed {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestTPMNotCompiledError(t *testing.T) {
	t.Run("Error", func(t *testing.T) {
		e := &TPMNotCompiledError{DevicePath: "/dev/tpm0"}
		want := "TPM support not compiled: build with -tags tpm to enable real TPM device at /dev/tpm0"
		if got := e.Error(); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("Constructor", func(t *testing.T) {
		de := NewTPMNotCompiledError("seal.init", "/dev/tpmrm0")
		if de.Code != dberrors.ErrConfiguration {
			t.Errorf("code = %v", de.Code)
		}
	})
}

func TestTPMCauseErrors(t *testing.T) {
	cause := errors.New("underlying")

	tests := []struct {
		name     string
		errFn    func() error
		newFn    func() *dberrors.DragonError
		wantMsg  string
		wantCode dberrors.ErrorCode
	}{
		{
			"TPMCrypto",
			func() error { return &TPMCryptoError{Operation: "encrypt", Cause: cause} },
			func() *dberrors.DragonError { return NewTPMCryptoError("tpm", "encrypt", cause) },
			"failed to encrypt: underlying",
			dberrors.ErrEncryption,
		},
		{
			"TPMInvalidSealedData",
			func() error { return &TPMInvalidSealedDataError{Cause: cause} },
			func() *dberrors.DragonError { return NewTPMInvalidSealedDataError("tpm.unseal", cause) },
			"invalid sealed data format: underlying",
			dberrors.ErrInvalidInput,
		},
		{
			"TPMDecryption",
			func() error { return &TPMDecryptionError{Cause: cause} },
			func() *dberrors.DragonError { return NewTPMDecryptionError("tpm.decrypt", cause) },
			"decryption failed: underlying",
			dberrors.ErrDecryption,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.errFn()
			if got := e.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			type unwrapper interface{ Unwrap() error }
			if uw, ok := e.(unwrapper); ok {
				if uw.Unwrap() != cause {
					t.Error("Unwrap failed")
				}
			}
			de := tt.newFn()
			if de.Code != tt.wantCode {
				t.Errorf("code = %v, want %v", de.Code, tt.wantCode)
			}
		})
	}
}
