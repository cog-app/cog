package gallery

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestShortCPU(t *testing.T) {
	for in, want := range map[string]string{
		"11th Gen Intel(R) Core(TM) i7-1165G7 @ 2.80GHz": "Intel Core i7-1165G7",
		"Intel(R) Core(TM) i5-10310U CPU @ 1.70GHz":      "Intel Core i5-10310U",
		"AMD Ryzen 5 5500":                       "AMD Ryzen 5 5500",
		"AMD Ryzen 7 5800H with Radeon Graphics": "AMD Ryzen 7 5800H",
		"amd64":                                  "amd64",
	} {
		require.Equal(t, want, shortCPU(in))
	}
}
