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

//go:build qrdb_upgrade_probe

package wire

// ProtocolV4Probe is the ALPN identifier an upgrade-probe (N+1) build
// prefers. Its framing is exactly ProtocolV3's; only the name differs, so
// the rolling-upgrade tests prove that a mixed N / N+1 pair negotiates down
// to ProtocolV3 (the release keeps exactly the previous identifier, as
// Protocols documents) while two N+1 nodes negotiate the newer name.
const ProtocolV4Probe = "qrdb-quic/4-probe"

func init() {
	Protocols = []string{ProtocolV4Probe, ProtocolV3}
}
