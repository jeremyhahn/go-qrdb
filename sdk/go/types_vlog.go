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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/vlog"

// ---------------------------------------------------------------------------
// Verifiable-log head-signing seam
//
// Every verifiable log signs its heads with a key a HeadSignerProvider
// creates and signs with. go-qrdb ships only the software provider, which
// keeps the key as the log's own sealed k/<version> record; an embedder that
// keeps its keys in hardware or a remote KMS implements HeadSignerProvider
// and passes it to node.WithHeadSignerProvider or, embedding the server
// directly, sdk/go/server.WithHeadSignerProvider. (Aliases pkg/vlog.)
// ---------------------------------------------------------------------------

// HeadSignerProvider creates a log's head-signing key and returns the signer
// for one of its versions. (Aliases pkg/vlog.HeadSignerProvider.)
type HeadSignerProvider = vlog.HeadSignerProvider

// HeadSigner signs one log's checkpoints with one key version. (Aliases
// pkg/vlog.HeadSigner.)
type HeadSigner = vlog.HeadSigner

// HeadSignerCreateKeyRequest names the log and the key a provider creates.
// (Aliases pkg/vlog.CreateKeyRequest.)
type HeadSignerCreateKeyRequest = vlog.CreateKeyRequest

// HeadSignerCreatedKey is what a provider returns for a created key: its
// reference, its public key and, from the software provider only, the sealed
// record the log stores. (Aliases pkg/vlog.CreatedKey.)
type HeadSignerCreatedKey = vlog.CreatedKey

// HeadSignerRequest names the log and the key version a signer is asked
// for. (Aliases pkg/vlog.HeadSignerRequest.)
type HeadSignerRequest = vlog.HeadSignerRequest

// HeadKey is one version of a log's head-signing key as its descriptor
// records it. (Aliases pkg/vlog.HeadKey.)
type HeadKey = vlog.HeadKey

// HeadSignatureType names a head-signing key's algorithm. (Aliases
// pkg/vlog.SignatureType.)
type HeadSignatureType = vlog.SignatureType

// The head-signing key algorithms. (Aliases pkg/vlog.Signature*.)
const (
	HeadSignatureEd25519         = vlog.SignatureEd25519
	HeadSignatureECDSAP256SHA256 = vlog.SignatureECDSAP256SHA256
)

// HeadKeyHash returns the signed-note key hash of a head-signing key, which
// a HeadSigner's KeyHash reports. (Aliases pkg/vlog.KeyHash.)
var HeadKeyHash = vlog.KeyHash
