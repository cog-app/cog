package feed

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

func TestM3ULauncherEscapes(t *testing.T) {
	assert.Equal(t, `"http://h/a%%20b?sig=x&skip=silence"`, desktopExecArg("http://h/a%20b?sig=x&skip=silence"))
	assert.Equal(t, `"a\"b\$c\\d\`+"`"+`e"`, desktopExecArg("a\"b$c\\d`e"))
}

func TestDesktopEntryFoldsNewlines(t *testing.T) {
	lines := strings.Split(desktopEntry("a\nb\r\nc\rd\x00e", "http://h/x"), "\n")
	assert.Equal(t, "Name=Cog: a b c d e", lines[2])
	assert.Equal(t, "Comment=Play a b c d e from Cog in mpv, silence skipped", lines[3])
	assert.Equal(t, 8, len(lines)) // 7 lines and the trailing newline
}

func TestLauncherSlug(t *testing.T) {
	assert.Equal(t, "morning-news", launcherSlug("Morning News"))
	assert.Equal(t, "a-b-c", launcherSlug(`  A"b/\c `))
	assert.Equal(t, "list", launcherSlug("日本語"))
	assert.Equal(t, "list", launcherSlug(""))
}
