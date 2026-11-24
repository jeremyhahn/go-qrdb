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

package sqlitevfs

import (
	"errors"
	"fmt"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// wireError rebuilds the server's typed write refusal from err, so
// pkg/sqlitevfs reads the outcome with errors.As whatever the transport.
//
// The embedded transport hands the server's error through unchanged, so a
// typed error already present is returned as is. A network transport
// carries the refusal as a reason and its metadata on a
// *transport.RequestError (errclass.serverWriteRefusal); the typed error is
// rebuilt from them and joined with err, so the wire detail stays readable.
// A status without a reason is never read as a refusal: REST sends 409 for
// both a guard refusal and a command conflict.
func wireError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*service.WriteGuardRefusedError](err); ok {
		return err
	}
	if _, ok := errors.AsType[*service.CommandConflictError](err); ok {
		return err
	}
	if _, ok := errors.AsType[*quota.ExceededError](err); ok {
		return err
	}
	if _, ok := errors.AsType[*quota.EntitlementError](err); ok {
		return err
	}
	re, ok := errors.AsType[*transport.RequestError](err)
	if !ok {
		return err
	}
	var typed error
	switch re.Reason {
	case service.ReasonWriteGuardRefused:
		typed = service.WriteGuardRefusedFromMetadata(re.Metadata)
	case service.ReasonCommandConflict:
		typed = service.CommandConflictFromMetadata(re.Metadata)
	case quota.ReasonQuotaExceeded:
		typed = quota.ExceededFromMetadata(re.Metadata)
	case quota.ReasonEntitlementDenied:
		typed = quota.EntitlementFromMetadata(re.Metadata)
	default:
		return err
	}
	return fmt.Errorf("%w: %w", typed, err)
}

// readError maps the transport's not-found outcomes of a read at a revision
// to the server's, so ServiceBackend.GetAt reads an absent key as not found
// rather than a failure.
func readError(err error, key string, revision int64) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*transport.KeyNotFoundError](err); ok {
		return fmt.Errorf("%w: %w", &service.KeyNotFoundError{Key: key}, err)
	}
	if _, ok := errors.AsType[*transport.RevisionNotFoundError](err); ok {
		return fmt.Errorf("%w: %w", &service.RevisionNotFoundError{Revision: revision}, err)
	}
	return wireError(err)
}
