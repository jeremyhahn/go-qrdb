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

//go:build qrdbd_webui

package main

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/api/rest"
)

//go:embed webui_dist/*
var webUIFS embed.FS

// webUIHandler returns an http.Handler that serves the embedded Svelte web UI
// with SPA fallback to index.html.
func webUIHandler() http.Handler {
	sub, err := fs.Sub(webUIFS, "webui_dist")
	if err != nil {
		panic("webui: failed to create sub filesystem: " + err.Error())
	}
	return rest.NewSPAHandler(sub)
}
