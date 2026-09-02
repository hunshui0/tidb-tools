package main

import (
	"testing"

	"github.com/pingcap/tidb-tools/sync_diff_inspector/source"
	"github.com/stretchr/testify/require"
)

func TestChecksumEqualRequiresSameAlgorithm(t *testing.T) {
	legacy := &source.ChecksumInfo{Checksum: 12}
	require.True(t, checksumEqual(legacy, &source.ChecksumInfo{Checksum: 12}))
	left := &source.ChecksumInfo{Algorithm: "CanonicalV1", Digest: [32]byte{1}}
	right := &source.ChecksumInfo{Algorithm: "CanonicalV1", Digest: [32]byte{1}}
	require.True(t, checksumEqual(left, right))
	right.Algorithm = "other"
	require.False(t, checksumEqual(left, right))
}
