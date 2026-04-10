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

package rest

import "net/http"

// configInitializedResponse is the JSON payload returned by
// GET /v1/config/initialized.
type configInitializedResponse struct {
	Initialized bool `json:"initialized"`
}

// handleConfigInitialized handles GET /v1/config/initialized -- returns
// whether the server has completed first-run setup (barrier initialized).
func (s *Server) handleConfigInitialized(w http.ResponseWriter, r *http.Request) {
	if s.barrierSvc == nil {
		writeJSON(w, configInitializedResponse{Initialized: false})
		return
	}

	info, err := s.barrierSvc.BarrierStatus(r.Context())
	if err != nil {
		writeJSON(w, configInitializedResponse{Initialized: false})
		return
	}

	writeJSON(w, configInitializedResponse{
		Initialized: !info.InitializedAt.IsZero(),
	})
}
