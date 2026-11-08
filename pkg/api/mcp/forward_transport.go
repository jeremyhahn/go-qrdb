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

package mcp

import (
	"errors"
	"net/http"

	mcpclient "github.com/mark3labs/mcp-go/client"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/mcphttp"
)

// errForwardRedirect is the forward's answer to any 3xx: every request
// carries a freshly signed on-behalf assertion, so following a peer's
// Location would hand that assertion to whichever host it names
// (GO-2026-6911).
var errForwardRedirect = errors.New("mcp forward: the peer answered with a redirect")

// newForwardClient returns an MCP client for the peer's endpoint that
// sends through rt and never follows a redirect: any 3xx is a forward
// failure.
func newForwardClient(endpoint string, rt http.RoundTripper) *mcpclient.Client {
	return mcpclient.NewClient(mcphttp.New("https://"+endpoint+"/mcp", &http.Client{
		Transport:     rt,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errForwardRedirect },
	}))
}
