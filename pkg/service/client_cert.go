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

package service

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
)

// VerifiedClientCertThumbprint returns the thumbprint of the request's
// verified client certificate, the certificate a share claim binds to: the
// leaf auth.VerifiedPeerCertificate resolves from the TLS state and the
// listener's verifier the transport bound into ctx. A certificate the
// connection presented but no one verified binds nothing and is refused as
// *ClientCertRequiredError, as no certificate at all is. A trust-state
// failure while verifying is returned as it is, so the caller retries.
func VerifiedClientCertThumbprint(ctx context.Context) (string, error) {
	leaf, err := auth.VerifiedPeerCertificate(ctx)
	if err != nil {
		if errors.As(err, new(*auth.TrustStateUnavailableError)) {
			return "", err
		}
		return "", &ClientCertRequiredError{}
	}
	return mtls.Thumbprint(leaf.Raw), nil
}
