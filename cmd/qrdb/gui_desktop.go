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

//go:build desktop

package main

import (
	"context"
	"embed"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

// launchGUI creates all Wails bindings and launches the desktop
// application window. It blocks until the user closes the window.
// The logFile parameter is the path from --log-file (empty when
// logging to stdout).
func launchGUI(log *logger.Logger, logFile string) error {
	log.Debug("creating Wails bindings")

	b := createGUIBindings(log, logFile)

	log.Debug("launching Wails application")

	return wails.Run(&options.App{
		Title:  "QRDB",
		Width:  1400,
		Height: 900,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: func(ctx context.Context) {
			log.Debug("Wails OnStartup")
			b.conn.startup(ctx)
			b.connManager.startup(ctx)
			b.server.startup(ctx)
			b.daemon.startup(ctx)
		},
		OnDomReady: func(_ context.Context) {
			log.Debug("Wails OnDomReady")
		},
		OnShutdown: func(ctx context.Context) {
			log.Debug("Wails OnShutdown")
			b.daemon.shutdown(ctx)
			b.connManager.shutdown(ctx)
			b.server.shutdown(ctx)
			b.conn.shutdown(ctx)
		},
		Bind: b.bindList(),
	})
}
