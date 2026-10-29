package codec

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

func TestTextEscaping(t *testing.T) {
	for _, s := range []string{"plain", "a, b; c\\d\nline", ""} {
		assert.Equal(t, s, UnescapeText(EscapeText(s)))
	}
	assert.Equal(t, "Room 1, Floor 2", UnescapeText("Room 1, Floor 2"))
	assert.Equal(t, `a\x`, UnescapeText(`a\x`))
	assert.Equal(t, "a\nb", UnescapeText(`a\Nb`))
}

func TestSplit(t *testing.T) {
	assert.Equal(t, []string{"Doe", "John", `Richter\, James`, "Mr.", ""}, Split(`Doe;John;Richter\, James;Mr.;`, ';'))
	assert.Equal(t, []string{"a", "b, c"}, SplitText(`a,b\, c`))
	assert.Equal(t, []string{""}, Split("", ','))
}

func TestLeftoversDrop(t *testing.T) {
	l := Leftovers{
		Props:  []contentline.Property{{Name: "X-PHONETIC-FIRST-NAME"}, {Name: "X-Other"}, {Name: "x-phonetic-last-name"}},
		Params: map[string][]contentline.Param{"N": {{Name: "SORT-AS", Values: []string{"a"}}, {Name: "X-A", Values: []string{"b"}}}},
	}
	c := l.Clone()
	l.Drop("X-PHONETIC-*")
	assert.Equal(t, 1, len(l.Props))
	assert.True(t, l.Has("x-other"))
	l.DropParams("N", "sort-as")
	assert.Equal(t, 1, len(l.Params["N"]))
	l.DropParams("N", "X-A")
	_, ok := l.Params["N"]
	assert.False(t, ok)
	assert.Equal(t, 3, len(c.Props))
	assert.Equal(t, 2, len(c.Params["N"]))
}
