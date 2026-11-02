package settings_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestSettings_GetSet(t *testing.T) {
	env := cogtest.New(t)
	s := env.Providers.Settings

	got, err := s.Get("does-not-exist")
	require.NoError(t, err)
	require.Equal(t, "", got)

	require.NoError(t, s.Set("hello", "world"))
	got, err = s.Get("hello")
	require.NoError(t, err)
	require.Equal(t, "world", got)

	// Upsert.
	require.NoError(t, s.Set("hello", "earth"))
	got, _ = s.Get("hello")
	require.Equal(t, "earth", got)
}

func TestSettings_BoolRoundtrip(t *testing.T) {
	env := cogtest.New(t)
	s := env.Providers.Settings

	require.False(t, s.GetBool("missing"))

	require.NoError(t, s.SetBool("flag", true))
	require.True(t, s.GetBool("flag"))

	require.NoError(t, s.SetBool("flag", false))
	require.False(t, s.GetBool("flag"))
}

func TestSettings_BoolNonTrueIsFalse(t *testing.T) {
	env := cogtest.New(t)
	s := env.Providers.Settings

	require.NoError(t, s.Set("flag", "yes")) // not "true"
	require.False(t, s.GetBool("flag"))
}

func TestSettings_IntRoundtrip(t *testing.T) {
	env := cogtest.New(t)
	s := env.Providers.Settings

	require.Equal(t, 42, s.GetInt("missing", 42))

	require.NoError(t, s.SetInt("n", 7))
	require.Equal(t, 7, s.GetInt("n", -1))
}

func TestSettings_IntBadValueReturnsDefault(t *testing.T) {
	env := cogtest.New(t)
	s := env.Providers.Settings

	require.NoError(t, s.Set("n", "not-an-int"))
	require.Equal(t, 99, s.GetInt("n", 99))
}

func TestSettings_NewStoreDirect(t *testing.T) {
	env := cogtest.New(t)

	// Construct a fresh Store against the same DB and assert it sees data
	// the Providers Store wrote.
	require.NoError(t, env.Providers.Settings.Set("shared", "ok"))
	s2 := settings.NewStore(env.Providers.DB)
	got, err := s2.Get("shared")
	require.NoError(t, err)
	require.Equal(t, "ok", got)
}

func TestSettings_ManyRoundtrip(t *testing.T) {
	env := cogtest.New(t)
	s := env.Providers.Settings

	require.NoError(t, s.Set("a", "old"))
	require.NoError(t, s.SetMany(map[string]string{"a": "1", "b": "2"}))

	got, err := s.GetMany("a", "b", "missing")
	require.NoError(t, err)
	require.Equal(t, "1", got["a"])
	require.Equal(t, "2", got["b"])
	require.Equal(t, "", got["missing"])
	require.Equal(t, 2, len(got))
}
