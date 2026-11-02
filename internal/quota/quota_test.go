package quota_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestSet(t *testing.T) {
	env := cogtest.New(t)
	store := env.Providers.Quotas

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///tmp/test", nil, "{}")
	require.NoError(t, err)

	// Insert.
	err = store.Set(vol.ID, "DEFAULT", "", 1024)
	require.NoError(t, err)

	q, err := store.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.NotNil(t, q)
	require.Equal(t, int64(1024), q.QuotaBytes)

	// Upsert — same volume/subject updates bytes.
	err = store.Set(vol.ID, "DEFAULT", "", 2048)
	require.NoError(t, err)

	q, err = store.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.Equal(t, int64(2048), q.QuotaBytes)
}

func TestGet(t *testing.T) {
	env := cogtest.New(t)
	store := env.Providers.Quotas

	// Not found returns nil, not error.
	q, err := store.Get("nonexistent", "DEFAULT", "")
	require.NoError(t, err)
	require.Nil(t, q)
}

func TestDelete(t *testing.T) {
	env := cogtest.New(t)
	store := env.Providers.Quotas

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///tmp/test", nil, "{}")
	require.NoError(t, err)

	err = store.Set(vol.ID, "DEFAULT", "", 1024)
	require.NoError(t, err)

	q, err := store.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.NotNil(t, q)

	err = store.Delete(q.ID)
	require.NoError(t, err)

	q, err = store.Get(vol.ID, "DEFAULT", "")
	require.NoError(t, err)
	require.Nil(t, q)
}

func TestListByVolume(t *testing.T) {
	env := cogtest.New(t)
	store := env.Providers.Quotas

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///tmp/test", nil, "{}")
	require.NoError(t, err)

	require.NoError(t, store.Set(vol.ID, "DEFAULT", "", 1000))
	require.NoError(t, store.Set(vol.ID, "USER", "u1", 2000))
	require.NoError(t, store.Set(vol.ID, "USER", "u2", 3000))

	var quotas []quota.Quota
	for q, err := range store.ListByVolume(vol.ID) {
		require.NoError(t, err)
		quotas = append(quotas, q)
	}

	require.Equal(t, 3, len(quotas))
	// Ordered by subject_type, subject_id: DEFAULT < USER/u1 < USER/u2.
	require.Equal(t, "DEFAULT", quotas[0].SubjectType)
	require.Equal(t, "USER", quotas[1].SubjectType)
	require.Equal(t, "u1", quotas[1].SubjectID)
}

func TestEffectiveQuota(t *testing.T) {
	env := cogtest.New(t)
	store := env.Providers.Quotas

	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///tmp/test", nil, "{}")
	require.NoError(t, err)

	// No quotas → unlimited (-1).
	got, err := store.EffectiveQuota("user1", vol.ID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(-1), got)

	// DEFAULT only.
	require.NoError(t, store.Set(vol.ID, "DEFAULT", "", 5000))
	got, err = store.EffectiveQuota("user1", vol.ID, nil)
	require.NoError(t, err)
	require.Equal(t, int64(5000), got)

	// GROUP overrides DEFAULT, picks max across groups.
	require.NoError(t, store.Set(vol.ID, "GROUP", "g1", 8000))
	require.NoError(t, store.Set(vol.ID, "GROUP", "g2", 10000))
	got, err = store.EffectiveQuota("user1", vol.ID, []string{"g1", "g2"})
	require.NoError(t, err)
	require.Equal(t, int64(10000), got)

	// USER overrides GROUP and DEFAULT.
	require.NoError(t, store.Set(vol.ID, "USER", "user1", 3000))
	got, err = store.EffectiveQuota("user1", vol.ID, []string{"g1", "g2"})
	require.NoError(t, err)
	require.Equal(t, int64(3000), got)
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{5368709120, "5.0 GB"},
	}
	for _, tt := range tests {
		got := quota.FormatBytes(tt.bytes)
		require.Equal(t, tt.want, got, "FormatBytes(%d)", tt.bytes)
	}
}
