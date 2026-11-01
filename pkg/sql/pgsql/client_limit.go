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

//go:build qrdb_pgsql

package pgsql

import (
	"context"
	"errors"

	wire "github.com/jeroenrinzema/psql-wire"
	"github.com/jeroenrinzema/psql-wire/codes"
	psqlerr "github.com/jeroenrinzema/psql-wire/errors"
	"github.com/jeroenrinzema/psql-wire/pkg/buffer"
	"github.com/jeroenrinzema/psql-wire/pkg/types"

	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
)

// WithClientLimiter charges every connection and every statement on this
// listener to the client-address bucket of l, before authentication. A
// refused connection receives a FATAL ErrorResponse with SQLSTATE 08004
// (sqlserver_rejected_establishment_of_sqlconnection) and is closed; a
// refused statement receives an ERROR ErrorResponse with SQLSTATE 53400
// (configuration_limit_exceeded) and the session stays open. A nil l limits
// nothing.
func WithClientLimiter(l *clientlimit.Limiter) ServerOption {
	return func(s *Server) {
		s.clientLimit = l
	}
}

// admitClient charges one request from the connection's peer address. The
// PostgreSQL wire carries no forwarding header, so the bucket is keyed by the
// peer address of the accepted connection. A refusal is returned as the
// ErrorResponse code and severity given; any other result passes through.
func admitClient(ctx context.Context, l *clientlimit.Limiter, code codes.Code, severity psqlerr.Severity) error {
	if l == nil {
		return nil
	}
	err := l.Admit(wire.RemoteAddress(ctx), nil)
	if _, limited := errors.AsType[*clientlimit.RateLimitedError](err); !limited {
		return err
	}
	return psqlerr.WithSeverity(psqlerr.WithCode(err, code), severity)
}

// authenticationOK is the AuthenticationOk message's status field: the
// authentication exchange completed successfully.
const authenticationOK int32 = 0

// clientLimitAuthStrategy refuses a connection whose client address is over
// its rate during the startup exchange, before the sealed-window gate and
// authentication run, with a FATAL ErrorResponse carrying SQLSTATE 08004.
func clientLimitAuthStrategy(l *clientlimit.Limiter) wire.AuthStrategy {
	return func(ctx context.Context, writer *buffer.Writer, _ *buffer.Reader) (context.Context, error) {
		if err := admitClient(ctx, l, codes.SQLserverRejectedEstablishmentOfSQLconnection, psqlerr.LevelFatal); err != nil {
			// A session-middleware error closes the connection without an
			// ErrorResponse; the authentication exchange is the last point
			// at which the protocol lets the server tell the client why.
			if werr := wire.WriteUnterminatedError(writer, err); werr != nil {
				return ctx, werr
			}
			return ctx, err
		}
		// AuthenticationOk: the strategy replaces psql-wire's default
		// exchange, so it ends the authentication phase itself. Identity is
		// established afterwards by the session middleware.
		writer.Start(types.ServerAuth)
		writer.AddInt32(authenticationOK)
		return ctx, writer.End()
	}
}

// admitStatement charges one statement -- a simple query, a Parse, or an
// Execute of a statement whose execution was deferred to it -- to the
// connection's client address.
func (h *ParseHandler) admitStatement(ctx context.Context) error {
	return admitClient(ctx, h.clientLimit, codes.ConfigurationLimitExceeded, psqlerr.LevelError)
}

// handleParseAdmitted is the parse function the wire server calls: it charges
// the message once, then parses it. HandleParse recurses into each statement
// of a multi-statement simple query; the charge is per message, here, so a
// client is not charged twice for the message that carries them.
func (h *ParseHandler) handleParseAdmitted(ctx context.Context, query wire.Query) (wire.PreparedStatements, error) {
	if err := h.admitStatement(ctx); err != nil {
		return nil, err
	}
	return h.HandleParse(ctx, query)
}
