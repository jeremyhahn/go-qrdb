// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefaultConfig validates that DefaultConfig returns sensible defaults.
func TestDefaultConfig(t *testing.T) {
	t.Run("DefaultValues", func(t *testing.T) {
		cfg := DefaultConfig()

		require.NotNil(t, cfg)
		assert.Equal(t, DefaultShardID, cfg.ShardID)
		assert.Equal(t, CodecJSON, cfg.CodecType)
		assert.Equal(t, DefaultTimeout, cfg.Timeout)
		assert.Equal(t, "", cfg.Partition)
	})

	t.Run("DefaultShardID", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, uint64(1), cfg.ShardID)
	})

	t.Run("DefaultCodecType", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, CodecJSON, cfg.CodecType)
	})

	t.Run("DefaultTimeout", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, 30*time.Second, cfg.Timeout)
	})

	t.Run("DefaultPartitionEmpty", func(t *testing.T) {
		cfg := DefaultConfig()
		assert.Equal(t, "", cfg.Partition)
	})
}

// TestWithShard validates the WithShard option.
func TestWithShard(t *testing.T) {
	t.Run("SetShardID", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithShard(5)
		opt(cfg)

		assert.Equal(t, uint64(5), cfg.ShardID)
	})

	t.Run("SetShardIDZero", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithShard(0)
		opt(cfg)

		assert.Equal(t, uint64(0), cfg.ShardID)
	})

	t.Run("SetShardIDLarge", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithShard(999999)
		opt(cfg)

		assert.Equal(t, uint64(999999), cfg.ShardID)
	})
}

// TestWithCodec validates the WithCodec option.
func TestWithCodec(t *testing.T) {
	t.Run("SetJSONCodec", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithCodec(CodecJSON)
		opt(cfg)

		assert.Equal(t, CodecJSON, cfg.CodecType)
	})

	t.Run("SetYAMLCodec", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithCodec(CodecYAML)
		opt(cfg)

		assert.Equal(t, CodecYAML, cfg.CodecType)
	})

	t.Run("SetTOMLCodec", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithCodec(CodecTOML)
		opt(cfg)

		assert.Equal(t, CodecTOML, cfg.CodecType)
	})

	t.Run("SetMsgPackCodec", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithCodec(CodecMsgPack)
		opt(cfg)

		assert.Equal(t, CodecMsgPack, cfg.CodecType)
	})

	t.Run("SetBSONCodec", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithCodec(CodecBSON)
		opt(cfg)

		assert.Equal(t, CodecBSON, cfg.CodecType)
	})

	t.Run("SetCBORCodec", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithCodec(CodecCBOR)
		opt(cfg)

		assert.Equal(t, CodecCBOR, cfg.CodecType)
	})

	t.Run("SetProtoBufCodec", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithCodec(CodecProtoBuf)
		opt(cfg)

		assert.Equal(t, CodecProtoBuf, cfg.CodecType)
	})
}

// TestWithTimeout validates the WithTimeout option.
func TestWithTimeout(t *testing.T) {
	t.Run("SetTimeout5Seconds", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithTimeout(5 * time.Second)
		opt(cfg)

		assert.Equal(t, 5*time.Second, cfg.Timeout)
	})

	t.Run("SetTimeout1Minute", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithTimeout(1 * time.Minute)
		opt(cfg)

		assert.Equal(t, 1*time.Minute, cfg.Timeout)
	})

	t.Run("SetTimeoutZero", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithTimeout(0)
		opt(cfg)

		assert.Equal(t, time.Duration(0), cfg.Timeout)
	})

	t.Run("SetTimeoutNegative", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithTimeout(-1 * time.Second)
		opt(cfg)

		assert.Equal(t, -1*time.Second, cfg.Timeout)
	})
}

// TestWithPartition validates the WithPartition option.
func TestWithPartition(t *testing.T) {
	t.Run("SetPartition", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithPartition("users")
		opt(cfg)

		assert.Equal(t, "users", cfg.Partition)
	})

	t.Run("SetPartitionEmpty", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Partition = "initial"
		opt := WithPartition("")
		opt(cfg)

		assert.Equal(t, "", cfg.Partition)
	})

	t.Run("SetPartitionWithSpecialChars", func(t *testing.T) {
		cfg := DefaultConfig()
		opt := WithPartition("user-data_v2")
		opt(cfg)

		assert.Equal(t, "user-data_v2", cfg.Partition)
	})

	t.Run("SetPartitionLong", func(t *testing.T) {
		cfg := DefaultConfig()
		longName := "very-long-partition-name-for-testing-purposes-123456789"
		opt := WithPartition(longName)
		opt(cfg)

		assert.Equal(t, longName, cfg.Partition)
	})
}

// TestParseOptions validates the ParseOptions function.
func TestParseOptions(t *testing.T) {
	t.Run("NoOptions", func(t *testing.T) {
		cfg := ParseOptions()

		require.NotNil(t, cfg)
		assert.Equal(t, DefaultShardID, cfg.ShardID)
		assert.Equal(t, CodecJSON, cfg.CodecType)
		assert.Equal(t, DefaultTimeout, cfg.Timeout)
		assert.Equal(t, "", cfg.Partition)
	})

	t.Run("SingleOption", func(t *testing.T) {
		cfg := ParseOptions(WithShard(10))

		require.NotNil(t, cfg)
		assert.Equal(t, uint64(10), cfg.ShardID)
		assert.Equal(t, CodecJSON, cfg.CodecType)
		assert.Equal(t, DefaultTimeout, cfg.Timeout)
		assert.Equal(t, "", cfg.Partition)
	})

	t.Run("MultipleOptions", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(20),
			WithCodec(CodecMsgPack),
			WithTimeout(15*time.Second),
		)

		require.NotNil(t, cfg)
		assert.Equal(t, uint64(20), cfg.ShardID)
		assert.Equal(t, CodecMsgPack, cfg.CodecType)
		assert.Equal(t, 15*time.Second, cfg.Timeout)
	})

	t.Run("OptionsOverwriteDefaults", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(100),
			WithCodec(CodecYAML),
			WithTimeout(5*time.Second),
		)

		require.NotNil(t, cfg)
		assert.NotEqual(t, DefaultShardID, cfg.ShardID)
		assert.NotEqual(t, CodecJSON, cfg.CodecType)
		assert.NotEqual(t, DefaultTimeout, cfg.Timeout)
	})

	t.Run("LastOptionWins", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(1),
			WithShard(2),
			WithShard(3),
		)

		require.NotNil(t, cfg)
		assert.Equal(t, uint64(3), cfg.ShardID)
	})

	t.Run("AllOptionsSet", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(777),
			WithCodec(CodecTOML),
			WithTimeout(45*time.Second),
		)

		require.NotNil(t, cfg)
		assert.Equal(t, uint64(777), cfg.ShardID)
		assert.Equal(t, CodecTOML, cfg.CodecType)
		assert.Equal(t, 45*time.Second, cfg.Timeout)
	})

	t.Run("PartialOptions", func(t *testing.T) {
		cfg := ParseOptions(
			WithCodec(CodecBSON),
		)

		require.NotNil(t, cfg)
		assert.Equal(t, DefaultShardID, cfg.ShardID)
		assert.Equal(t, CodecBSON, cfg.CodecType)
		assert.Equal(t, DefaultTimeout, cfg.Timeout)
	})

	t.Run("WithPartitionOption", func(t *testing.T) {
		cfg := ParseOptions(
			WithPartition("users"),
		)

		require.NotNil(t, cfg)
		assert.Equal(t, "users", cfg.Partition)
		assert.Equal(t, DefaultShardID, cfg.ShardID)
	})

	t.Run("AllOptionsIncludingPartition", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(5),
			WithCodec(CodecJSON),
			WithTimeout(10*time.Second),
			WithPartition("sessions"),
		)

		require.NotNil(t, cfg)
		assert.Equal(t, uint64(5), cfg.ShardID)
		assert.Equal(t, CodecJSON, cfg.CodecType)
		assert.Equal(t, 10*time.Second, cfg.Timeout)
		assert.Equal(t, "sessions", cfg.Partition)
	})
}

// TestOptionCombinations validates various combinations of options.
func TestOptionCombinations(t *testing.T) {
	t.Run("ShardAndCodec", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(50),
			WithCodec(CodecMsgPack),
		)

		assert.Equal(t, uint64(50), cfg.ShardID)
		assert.Equal(t, CodecMsgPack, cfg.CodecType)
		assert.Equal(t, DefaultTimeout, cfg.Timeout)
	})

	t.Run("ShardAndTimeout", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(60),
			WithTimeout(20*time.Second),
		)

		assert.Equal(t, uint64(60), cfg.ShardID)
		assert.Equal(t, CodecJSON, cfg.CodecType)
		assert.Equal(t, 20*time.Second, cfg.Timeout)
	})

	t.Run("CodecAndTimeout", func(t *testing.T) {
		cfg := ParseOptions(
			WithCodec(CodecYAML),
			WithTimeout(25*time.Second),
		)

		assert.Equal(t, DefaultShardID, cfg.ShardID)
		assert.Equal(t, CodecYAML, cfg.CodecType)
		assert.Equal(t, 25*time.Second, cfg.Timeout)
	})

	t.Run("AllThreeOptions", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(888),
			WithCodec(CodecBSON),
			WithTimeout(60*time.Second),
		)

		assert.Equal(t, uint64(888), cfg.ShardID)
		assert.Equal(t, CodecBSON, cfg.CodecType)
		assert.Equal(t, 60*time.Second, cfg.Timeout)
	})

	t.Run("ShardAndPartition", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(10),
			WithPartition("users"),
		)

		assert.Equal(t, uint64(10), cfg.ShardID)
		assert.Equal(t, "users", cfg.Partition)
		assert.Equal(t, CodecJSON, cfg.CodecType)
	})

	t.Run("PartitionAndCodec", func(t *testing.T) {
		cfg := ParseOptions(
			WithPartition("sessions"),
			WithCodec(CodecMsgPack),
		)

		assert.Equal(t, "sessions", cfg.Partition)
		assert.Equal(t, CodecMsgPack, cfg.CodecType)
		assert.Equal(t, DefaultShardID, cfg.ShardID)
	})

	t.Run("AllFourOptions", func(t *testing.T) {
		cfg := ParseOptions(
			WithShard(100),
			WithCodec(CodecYAML),
			WithTimeout(30*time.Second),
			WithPartition("data"),
		)

		assert.Equal(t, uint64(100), cfg.ShardID)
		assert.Equal(t, CodecYAML, cfg.CodecType)
		assert.Equal(t, 30*time.Second, cfg.Timeout)
		assert.Equal(t, "data", cfg.Partition)
	})
}

// TestConfigConstants validates the exported constants.
func TestConfigConstants(t *testing.T) {
	t.Run("DefaultShardID", func(t *testing.T) {
		assert.Equal(t, uint64(1), DefaultShardID)
	})

	t.Run("DefaultTimeout", func(t *testing.T) {
		assert.Equal(t, 30*time.Second, DefaultTimeout)
	})

	t.Run("CodecTypes", func(t *testing.T) {
		assert.Equal(t, CodecType("json"), CodecJSON)
		assert.Equal(t, CodecType("yaml"), CodecYAML)
		assert.Equal(t, CodecType("toml"), CodecTOML)
		assert.Equal(t, CodecType("msgpack"), CodecMsgPack)
		assert.Equal(t, CodecType("protobuf"), CodecProtoBuf)
		assert.Equal(t, CodecType("bson"), CodecBSON)
		assert.Equal(t, CodecType("cbor"), CodecCBOR)
	})
}

// TestOptionImmutability validates that options don't affect each other.
func TestOptionImmutability(t *testing.T) {
	t.Run("IndependentConfigs", func(t *testing.T) {
		cfg1 := ParseOptions(WithShard(1))
		cfg2 := ParseOptions(WithShard(2))

		assert.Equal(t, uint64(1), cfg1.ShardID)
		assert.Equal(t, uint64(2), cfg2.ShardID)
	})

	t.Run("ModifyingOneDoesNotAffectOther", func(t *testing.T) {
		cfg1 := ParseOptions(WithShard(10))
		cfg2 := ParseOptions(WithShard(20))

		cfg1.ShardID = 999

		assert.Equal(t, uint64(999), cfg1.ShardID)
		assert.Equal(t, uint64(20), cfg2.ShardID)
	})

	t.Run("IndependentPartitionConfigs", func(t *testing.T) {
		cfg1 := ParseOptions(WithPartition("users"))
		cfg2 := ParseOptions(WithPartition("sessions"))

		assert.Equal(t, "users", cfg1.Partition)
		assert.Equal(t, "sessions", cfg2.Partition)
	})

	t.Run("ModifyingPartitionDoesNotAffectOther", func(t *testing.T) {
		cfg1 := ParseOptions(WithPartition("users"))
		cfg2 := ParseOptions(WithPartition("sessions"))

		cfg1.Partition = "modified"

		assert.Equal(t, "modified", cfg1.Partition)
		assert.Equal(t, "sessions", cfg2.Partition)
	})
}

// TestOptionOrdering validates that option order doesn't matter (except for duplicates).
func TestOptionOrdering(t *testing.T) {
	t.Run("DifferentOrderSameResult", func(t *testing.T) {
		cfg1 := ParseOptions(
			WithShard(5),
			WithCodec(CodecMsgPack),
			WithTimeout(10*time.Second),
		)

		cfg2 := ParseOptions(
			WithTimeout(10*time.Second),
			WithCodec(CodecMsgPack),
			WithShard(5),
		)

		assert.Equal(t, cfg1.ShardID, cfg2.ShardID)
		assert.Equal(t, cfg1.CodecType, cfg2.CodecType)
		assert.Equal(t, cfg1.Timeout, cfg2.Timeout)
	})

	t.Run("DuplicateOptionsLastWins", func(t *testing.T) {
		cfg := ParseOptions(
			WithCodec(CodecJSON),
			WithCodec(CodecYAML),
			WithCodec(CodecMsgPack),
		)

		assert.Equal(t, CodecMsgPack, cfg.CodecType)
	})

	t.Run("DifferentOrderWithPartition", func(t *testing.T) {
		cfg1 := ParseOptions(
			WithPartition("users"),
			WithShard(5),
			WithCodec(CodecMsgPack),
		)

		cfg2 := ParseOptions(
			WithCodec(CodecMsgPack),
			WithPartition("users"),
			WithShard(5),
		)

		assert.Equal(t, cfg1.ShardID, cfg2.ShardID)
		assert.Equal(t, cfg1.CodecType, cfg2.CodecType)
		assert.Equal(t, cfg1.Partition, cfg2.Partition)
	})

	t.Run("DuplicatePartitionLastWins", func(t *testing.T) {
		cfg := ParseOptions(
			WithPartition("users"),
			WithPartition("sessions"),
			WithPartition("final"),
		)

		assert.Equal(t, "final", cfg.Partition)
	})
}
