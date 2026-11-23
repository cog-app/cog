package gallery

import (
	"fmt"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func move(from, to string) RenamePreview { return RenamePreview{OldPath: from, NewPath: to} }

// Renames that differ in their numbers, or in one folder, are one kind;
// a kind widens only once, so neighbours do not chain into "*/*/*".
func TestSummarizeRenamesGroupsByShape(t *testing.T) {
	var renames []RenamePreview
	for i := range 30 {
		renames = append(renames, move(fmt.Sprintf("/Photos/phone/20%02d/%02d/IMG_%d.jpg", i%10, i%12+1, i), fmt.Sprintf("/Photos/20%02d/%02d/IMG_%d.jpg", i%10, i%12+1, i)))
	}
	renames = append(renames,
		move("/Photos/phone/Trip/a.jpg", "/Photos/2020/01/a.jpg"),
		move("/Photos/phone/Party/b.jpg", "/Photos/2020/01/b.jpg"),
		move("/Photos/phone/Party/Mine/c.jpg", "/Photos/2020/01/c.jpg"),
		move("/Photos/phone/Beach/Theirs/d.jpg", "/Photos/2020/01/d.jpg"),
		move("/Photos/camera/Party/e.jpg", "/Photos/2020/01/e.jpg"),
		RenamePreview{OldPath: "/Photos/phone/Trip/f.jpg", NewPath: "/Photos/2020/01/f_1.jpg", Numbered: true},
	)
	sum := SummarizeRenames(renames, 100)
	var got []string
	for _, k := range sum.Kinds {
		got = append(got, fmt.Sprintf("%d %d %s -> %s", k.Count, k.Numbered, k.From, k.To))
	}
	require.Equal(t, []string{
		"30 0 /Photos/phone/#/#/{name} -> /Photos/#/#/{name}",
		"3 1 /Photos/phone/*/{name} -> /Photos/#/#/{name}",
		"1 0 /Photos/phone/Party/Mine/{name} -> /Photos/#/#/{name}",
		"1 0 /Photos/phone/Beach/Theirs/{name} -> /Photos/#/#/{name}",
		"1 0 /Photos/camera/Party/{name} -> /Photos/#/#/{name}",
	}, got)
	require.Equal(t, "/Photos/phone/2000/01/IMG_0.jpg", sum.Kinds[0].Example.OldPath)
	require.Equal(t, 0, sum.Other)
}

func TestSummarizeRenamesStopsAtLimit(t *testing.T) {
	renames := []RenamePreview{
		move("/a/x.jpg", "/P/x.jpg"),
		move("/b/c/x.jpg", "/P/x.jpg"),
		move("/d/e/f/x.jpg", "/P/x.jpg"),
		move("/a/y.jpg", "/P/y.jpg"),
	}
	sum := SummarizeRenames(renames, 2)
	require.Equal(t, 2, len(sum.Kinds))
	require.Equal(t, 2, sum.Kinds[0].Count)
	require.Equal(t, 1, sum.Other)
}

func TestSummarizeSkipsByReason(t *testing.T) {
	kinds := SummarizeSkips([]RenameSkip{
		{"/a.jpg", "“/x/a.jpg” is already taken."},
		{"/b.jpg", "“/x/b.jpg” is already taken."},
		{"/c.jpg", "The pattern failed: template: bad"},
	})
	require.Equal(t, 2, len(kinds))
	require.Equal(t, "… is already taken.", kinds[0].Reason)
	require.Equal(t, 2, kinds[0].Count)
	require.Equal(t, "The pattern failed", kinds[1].Reason)
}
