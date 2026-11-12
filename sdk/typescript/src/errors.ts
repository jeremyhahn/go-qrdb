// Copyright 2026 Jeremy Hahn
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

// ---------------------------------------------------------------------------
// Base error
// ---------------------------------------------------------------------------

/** Base error class for all QRDB SDK errors. */
export class QRDBError extends Error {
    constructor(message: string) {
        super(message);
        this.name = 'QRDBError';
    }
}

// ---------------------------------------------------------------------------
// Transport errors
// ---------------------------------------------------------------------------

/** Raised when the transport configuration is invalid. */
export class ConfigError extends QRDBError {
    /** The invalid configuration field name. */
    readonly field: string;
    /** Description of why the field is invalid. */
    readonly reason: string;

    constructor(field: string, reason: string) {
        super(`transport: invalid config field ${field}: ${reason}`);
        this.name = 'ConfigError';
        this.field = field;
        this.reason = reason;
    }
}

/** Raised when a transport connection operation fails. */
export class ConnectionError extends QRDBError {
    /** The address that failed to connect. */
    readonly address: string;
    /** The underlying error, if available. */
    readonly cause: Error | undefined;

    constructor(address: string, cause?: Error) {
        const msg = cause
            ? `transport: connection failed to ${address}: ${cause.message}`
            : `transport: connection failed to ${address}`;
        super(msg);
        this.name = 'ConnectionError';
        this.address = address;
        this.cause = cause;
    }
}

/** Raised when an operation requires a connection that has not been established. */
export class NotConnectedError extends QRDBError {
    constructor() {
        super('transport: connection is closed');
        this.name = 'NotConnectedError';
    }
}

/** Raised when an operation is attempted on a closed transport. */
export class ClosedError extends QRDBError {
    constructor() {
        super('transport: connection is closed');
        this.name = 'ClosedError';
    }
}

/** Raised when an HTTP request or transport RPC fails. */
export class TransportError extends QRDBError {
    /** The transport method that failed. */
    readonly method: string;
    /** The HTTP status code, if available. */
    readonly statusCode: number | undefined;
    /** The underlying error, if available. */
    readonly cause: Error | undefined;

    constructor(method: string, statusCode?: number, cause?: Error) {
        let msg = `transport: request ${method} failed`;
        if (statusCode !== undefined && statusCode !== 0) {
            msg += ` (status ${statusCode})`;
        }
        if (cause) {
            msg += `: ${cause.message}`;
        }
        super(msg);
        this.name = 'TransportError';
        this.method = method;
        this.statusCode = statusCode;
        this.cause = cause;
    }
}

/** Raised when TLS configuration or handshake fails. */
export class TLSError extends QRDBError {
    /** The underlying error. */
    readonly cause: Error;

    constructor(cause: Error) {
        super(`transport: TLS error: ${cause.message}`);
        this.name = 'TLSError';
        this.cause = cause;
    }
}

// ---------------------------------------------------------------------------
// Service errors
// ---------------------------------------------------------------------------

/** Raised when the target node is not the Raft leader. */
export class NotLeaderError extends QRDBError {
    /** The shard that rejected the request. */
    readonly shardId: number;
    /** The replica ID of the current leader (0 if unknown). */
    readonly leaderId: number;
    /** The Raft transport address of the current leader. */
    readonly leaderAddress: string;

    constructor(shardId: number, leaderId: number, leaderAddress: string) {
        const msg = leaderId === 0
            ? `transport: not leader for shard ${shardId}, leader unknown`
            : `transport: not leader for shard ${shardId}, leader is replica ${leaderId} at ${leaderAddress}`;
        super(msg);
        this.name = 'NotLeaderError';
        this.shardId = shardId;
        this.leaderId = leaderId;
        this.leaderAddress = leaderAddress;
    }
}

/** Raised when a key is not found in the store. */
export class KeyNotFoundError extends QRDBError {
    /** The key that was not found. */
    readonly key: string;

    constructor(key: string) {
        super(`transport: key not found: ${key}`);
        this.name = 'KeyNotFoundError';
        this.key = key;
    }
}

/** Raised when a requested revision has been compacted and is no longer available. */
export class RevisionCompactedError extends QRDBError {
    /** The revision that was compacted. */
    readonly revision: number;

    constructor(revision: number) {
        super(`transport: revision ${revision} has been compacted`);
        this.name = 'RevisionCompactedError';
        this.revision = revision;
    }
}

/** Raised when a requested revision does not exist. */
export class RevisionNotFoundError extends QRDBError {
    /** The revision that was not found. */
    readonly revision: number;

    constructor(revision: number) {
        super(`transport: revision ${revision} not found`);
        this.name = 'RevisionNotFoundError';
        this.revision = revision;
    }
}

/** Raised when a transaction's preconditions did not pass. */
export class TransactionFailedError extends QRDBError {
    /** The revision at which the transaction failed. */
    readonly revision: number;

    constructor(revision: number) {
        super(`transport: transaction preconditions failed at revision ${revision}`);
        this.name = 'TransactionFailedError';
        this.revision = revision;
    }
}

// ---------------------------------------------------------------------------
// Lease errors
// ---------------------------------------------------------------------------

/** Raised when a lease ID does not exist. */
export class LeaseNotFoundError extends QRDBError {
    /** The lease ID that was not found. */
    readonly leaseId: number;

    constructor(leaseId: number) {
        super(`transport: lease not found: ${leaseId}`);
        this.name = 'LeaseNotFoundError';
        this.leaseId = leaseId;
    }
}

/** Raised when a lease has already expired. */
export class LeaseExpiredError extends QRDBError {
    /** The expired lease ID. */
    readonly leaseId: number;

    constructor(leaseId: number) {
        super(`transport: lease expired: ${leaseId}`);
        this.name = 'LeaseExpiredError';
        this.leaseId = leaseId;
    }
}

// ---------------------------------------------------------------------------
// Watch errors
// ---------------------------------------------------------------------------

/** Raised when a watch stream is canceled. */
export class WatchCanceledError extends QRDBError {
    /** The reason for cancellation, if provided. */
    readonly reason: string;

    constructor(reason?: string) {
        const msg = reason
            ? `transport: watch canceled: ${reason}`
            : 'transport: watch canceled';
        super(msg);
        this.name = 'WatchCanceledError';
        this.reason = reason ?? '';
    }
}

// ---------------------------------------------------------------------------
// Index errors
// ---------------------------------------------------------------------------

/** Raised when attempting to create an index that already exists. */
export class IndexExistsError extends QRDBError {
    /** The index name that already exists. */
    readonly indexName: string;

    constructor(indexName: string) {
        super(`transport: index already exists: ${indexName}`);
        this.name = 'IndexExistsError';
        this.indexName = indexName;
    }
}

/** Raised when a referenced index does not exist. */
export class IndexNotFoundError extends QRDBError {
    /** The index name that was not found. */
    readonly indexName: string;

    constructor(indexName: string) {
        super(`transport: index not found: ${indexName}`);
        this.name = 'IndexNotFoundError';
        this.indexName = indexName;
    }
}

// ---------------------------------------------------------------------------
// Vector errors
// ---------------------------------------------------------------------------

/** Raised when a vector ID does not exist in a namespace. */
export class VectorNotFoundError extends QRDBError {
    /** The namespace containing the missing vector. */
    readonly namespace: string;
    /** The vector ID that was not found. */
    readonly vectorId: string;

    constructor(namespace: string, vectorId: string) {
        super(`transport: vector not found: ${namespace}/${vectorId}`);
        this.name = 'VectorNotFoundError';
        this.namespace = namespace;
        this.vectorId = vectorId;
    }
}

/** Raised when a vector namespace does not exist. */
export class NamespaceNotFoundError extends QRDBError {
    /** The namespace that was not found. */
    readonly namespace: string;

    constructor(namespace: string) {
        super(`transport: namespace not found: ${namespace}`);
        this.name = 'NamespaceNotFoundError';
        this.namespace = namespace;
    }
}

/** Raised when attempting to create a namespace that already exists. */
export class NamespaceExistsError extends QRDBError {
    /** The namespace that already exists. */
    readonly namespace: string;

    constructor(namespace: string) {
        super(`transport: namespace already exists: ${namespace}`);
        this.name = 'NamespaceExistsError';
        this.namespace = namespace;
    }
}

/** Raised when a vector's dimensionality does not match the namespace configuration. */
export class DimensionMismatchError extends QRDBError {
    /** The expected vector dimension. */
    readonly expected: number;
    /** The actual vector dimension provided. */
    readonly got: number;

    constructor(expected: number, got: number) {
        super(`transport: dimension mismatch: expected ${expected}, got ${got}`);
        this.name = 'DimensionMismatchError';
        this.expected = expected;
        this.got = got;
    }
}

// ---------------------------------------------------------------------------
// User errors
// ---------------------------------------------------------------------------

/** Raised when a user ID does not exist. */
export class UserNotFoundError extends QRDBError {
    /** The user ID that was not found. */
    readonly userId: string;

    constructor(userId: string) {
        super(`transport: user not found: ${userId}`);
        this.name = 'UserNotFoundError';
        this.userId = userId;
    }
}

/** Raised when a username already exists within the tenant. */
export class UserExistsError extends QRDBError {
    /** The username that already exists. */
    readonly username: string;

    constructor(username: string) {
        super(`transport: user already exists: ${username}`);
        this.name = 'UserExistsError';
        this.username = username;
    }
}

// ---------------------------------------------------------------------------
// Role errors
// ---------------------------------------------------------------------------

/** Raised when a role ID does not exist. */
export class RoleNotFoundError extends QRDBError {
    /** The role ID that was not found. */
    readonly roleId: string;

    constructor(roleId: string) {
        super(`transport: role not found: ${roleId}`);
        this.name = 'RoleNotFoundError';
        this.roleId = roleId;
    }
}

/** Raised when a role name already exists within the tenant. */
export class RoleExistsError extends QRDBError {
    /** The role name that already exists. */
    readonly roleName: string;

    constructor(roleName: string) {
        super(`transport: role already exists: ${roleName}`);
        this.name = 'RoleExistsError';
        this.roleName = roleName;
    }
}

// ---------------------------------------------------------------------------
// Certificate errors
// ---------------------------------------------------------------------------

/** Raised when a certificate serial does not exist. */
export class CertNotFoundError extends QRDBError {
    /** The certificate serial that was not found. */
    readonly serial: string;

    constructor(serial: string) {
        super(`transport: certificate not found: ${serial}`);
        this.name = 'CertNotFoundError';
        this.serial = serial;
    }
}

/**
 * Thrown when a PKCS#12 archive cannot be built from the given certificate
 * and key: a missing or malformed PEM block, an unsupported key encoding,
 * or a crypto failure. The archive is always built locally (no server has
 * a PKCS#12 route), so this error never comes from the network.
 */
export class CertExportError extends QRDBError {
    /** Description of why the export failed. */
    readonly reason: string;

    constructor(reason: string) {
        super(`cert: export: ${reason}`);
        this.name = 'CertExportError';
        this.reason = reason;
    }
}

// ---------------------------------------------------------------------------
// Feature errors
// ---------------------------------------------------------------------------

/** Raised when a compile-time feature is not enabled in the server binary. */
export class FeatureDisabledError extends QRDBError {
    /** The feature that is not available. */
    readonly feature: string;

    constructor(feature: string) {
        super(`feature not available: ${feature}`);
        this.name = 'FeatureDisabledError';
        this.feature = feature;
    }
}

/** Raised when a feature is only available in desktop (Wails) mode. */
export class DesktopOnlyError extends QRDBError {
    /** The feature that requires desktop mode. */
    readonly feature: string;

    constructor(feature: string) {
        super(`${feature} is only available in desktop mode`);
        this.name = 'DesktopOnlyError';
        this.feature = feature;
    }
}

/**
 * Raised when the first request of a Shamir tenant barrier init answers
 * without the roster and unseal key check value the second must echo: the
 * node prepared no split, and the barrier is not initialized.
 */
export class ShamirSplitMissingError extends QRDBError {
    /** The tenant whose barrier init was refused. */
    readonly tenantId: string;

    constructor(tenantId: string) {
        super(`transport: shamir barrier init of tenant "${tenantId}" returned no unseal key check value or roster to sign`);
        this.name = 'ShamirSplitMissingError';
        this.tenantId = tenantId;
    }
}

/**
 * Raised when the second request of a Shamir tenant barrier init is answered
 * with a split other than the one it echoed: the node did not complete the
 * split the first request prepared, so the init is not reported done.
 */
export class ShamirSplitMismatchError extends QRDBError {
    /** The tenant whose barrier init was refused. */
    readonly tenantId: string;
    /** The field that differs: "roster" or "unseal key check". */
    readonly field: string;

    constructor(tenantId: string, field: string) {
        super(`transport: shamir barrier init of tenant "${tenantId}" completed a different ${field} than it prepared`);
        this.name = 'ShamirSplitMismatchError';
        this.tenantId = tenantId;
        this.field = field;
    }
}

/** Raised when a transport method is not yet implemented. */
export class NotImplementedError extends QRDBError {
    /** The method that is not implemented. */
    readonly method: string;

    constructor(method: string) {
        super(`transport: method not implemented: ${method}`);
        this.name = 'NotImplementedError';
        this.method = method;
    }
}
