package compat

import (
	"encoding/xml"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestParseIf(t *testing.T) {
	lists, err := parseIf(`(<opaquelocktoken:a> ["e1"]) (Not <DAV:no-lock>)`)
	require.NoError(t, err)
	require.Equal(t, 2, len(lists))
	require.Equal(t, []ifCondition{{token: "opaquelocktoken:a"}, {etag: `"e1"`}}, lists[0].conds)
	require.Equal(t, []ifCondition{{not: true, token: "DAV:no-lock"}}, lists[1].conds)

	// Tagged lists apply to the resource before them, until the next tag.
	// An entity tag may hold "]"; and weak ones are allowed.
	lists, err = parseIf(`<https://h/remote.php/dav/files/u/a.txt> (["x]y"]) ([W/"w"]) <https://h/b> (<t>)`)
	require.NoError(t, err)
	require.Equal(t, 3, len(lists))
	require.Equal(t, "https://h/remote.php/dav/files/u/a.txt", lists[0].resource)
	require.Equal(t, `"x]y"`, lists[0].conds[0].etag)
	require.Equal(t, "https://h/remote.php/dav/files/u/a.txt", lists[1].resource)
	require.Equal(t, `W/"w"`, lists[1].conds[0].etag)
	require.Equal(t, "https://h/b", lists[2].resource)

	for _, bad := range []string{`<x>`, `()`, `(<t>`, `([e1])`, `junk`, `(Nothing)`, `<> (<t>)`} {
		_, err := parseIf(bad)
		require.Error(t, err)
	}
}

func TestETagMatches(t *testing.T) {
	require.True(t, etagMatches(`"abc"`, "abc"))
	require.True(t, etagMatches(` W/"abc"`, "abc"))
	require.False(t, etagMatches(`abc`, "abc"))
	require.True(t, etagListMatches(`"x", "abc"`, "abc"))
}

func TestLockInfoOwner(t *testing.T) {
	// The owner's prefix is bound on the request's root, which is not
	// given back with it.
	var info lockInfo
	require.NoError(t, xml.Unmarshal([]byte(`<a:lockinfo xmlns:a="DAV:"><a:lockscope><a:shared/></a:lockscope><a:locktype><a:write/></a:locktype>
		<a:owner><a:href>mailto:x@y</a:href></a:owner></a:lockinfo>`), &info))
	require.Equal(t, `<href xmlns="DAV:">mailto:x@y</href>`, info.Owner.XML)
	require.True(t, info.LockScope.Shared != nil)

	require.NoError(t, xml.Unmarshal([]byte(`<lockinfo xmlns="DAV:"><owner>Just text &amp; more</owner></lockinfo>`), &info))
	require.Equal(t, `Just text &amp; more`, info.Owner.XML)
}
