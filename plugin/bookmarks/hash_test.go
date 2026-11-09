package bookmarks

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// Reference values from the algorithms' published test vectors.
func TestHashFunctions(t *testing.T) {
	require.Equal(t, uint32(0), murmur3(""))
	require.Equal(t, uint32(0x248bfa47), murmur3("hello"))
	require.Equal(t, uint32(0x2e4ff723), murmur3("The quick brown fox jumps over the lazy dog"))

	require.Equal(t, uint32(0x02cc5d05), xxh32(""))
	require.Equal(t, uint32(0x32d153ff), xxh32("abc"))
	require.Equal(t, uint32(0xe2293b2f), xxh32("Nobody inspects the spammish repetition"))

	// Floccus writes xxh32 without leading zeros, murmur3 with them.
	require.Equal(t, "2cc5d05", hashers["xxh32"](""))
	require.Equal(t, "00000000", hashers["murmur3a"](""))
}

func TestWriteJSONString(t *testing.T) {
	var b strings.Builder
	writeJSONString(&b, "a/b <&>   ü \"q\" \\ \b\f\n\r\t \x01")
	// What JSON.stringify gives.
	require.Equal(t, `"a/b <&> `+" "+` ü \"q\" \\ \b\f\n\r\t \u0001"`, b.String())
}
