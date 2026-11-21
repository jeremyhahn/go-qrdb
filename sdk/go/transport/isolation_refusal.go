package transport

import (
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ReasonFeatureNotActive is the refusal reason a server sends with a
// *feature.NotActiveError, so every network transport returns the client
// the typed error rather than a bare Unavailable status.
const ReasonFeatureNotActive = "FEATURE_NOT_ACTIVE"

// ReasonInvalidIsolationPolicy is the refusal reason a server sends with a
// *tenant.InvalidIsolationPolicyError.
const ReasonInvalidIsolationPolicy = "INVALID_ISOLATION_POLICY"

const (
	featureNotActiveKeyFeature     = "feature"
	featureNotActiveKeyShardID     = "shard_id"
	invalidIsolationPolicyKeyValue = "value"
)

// FeatureNotActiveMetadata encodes e as refusal metadata.
func FeatureNotActiveMetadata(e *feature.NotActiveError) map[string]string {
	return map[string]string{
		featureNotActiveKeyFeature: string(e.Feature),
		featureNotActiveKeyShardID: strconv.FormatUint(e.ShardID, 10),
	}
}

// FeatureNotActiveFromMetadata decodes the refusal metadata
// FeatureNotActiveMetadata encodes. A missing or malformed shard id reads
// as zero, the cluster-wide form.
func FeatureNotActiveFromMetadata(md map[string]string) *feature.NotActiveError {
	shard, err := strconv.ParseUint(md[featureNotActiveKeyShardID], 10, 64)
	if err != nil {
		shard = 0
	}
	return &feature.NotActiveError{Feature: feature.Feature(md[featureNotActiveKeyFeature]), ShardID: shard}
}

// InvalidIsolationPolicyMetadata encodes e as refusal metadata.
func InvalidIsolationPolicyMetadata(e *tenant.InvalidIsolationPolicyError) map[string]string {
	return map[string]string{invalidIsolationPolicyKeyValue: e.Value}
}

// InvalidIsolationPolicyFromMetadata decodes the refusal metadata
// InvalidIsolationPolicyMetadata encodes.
func InvalidIsolationPolicyFromMetadata(md map[string]string) *tenant.InvalidIsolationPolicyError {
	return &tenant.InvalidIsolationPolicyError{Value: md[invalidIsolationPolicyKeyValue]}
}
