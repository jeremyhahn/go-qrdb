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

package capabilities

import "log/slog"

// WarnUnencryptedAtRest logs one WARNING per entry of c.UnencryptedAtRest.
// Composition roots call it once at boot with the profile they seal, so an
// operator who enabled seal learns at startup -- not from an audit -- that
// part of the node's data is stored unencrypted. It logs nothing when the
// profile reports no gap.
func (c Capabilities) WarnUnencryptedAtRest(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	for _, gap := range c.UnencryptedAtRest {
		logger.Warn("seal is enabled but data is stored unencrypted at rest", slog.String("where", gap))
	}
}
