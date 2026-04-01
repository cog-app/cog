package media_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/plugin/media"
)

func TestImportOPML(t *testing.T) {
	env := cogtest.New(t, media.New())
	admin := env.CreateUser(t, "admin", "pass")
	env.Get(t, "/apps/media/settings", admin.Auth).Status(200).Contains("Import")
}

func TestImportPageFormats(t *testing.T) {
	env := cogtest.New(t, media.New())
	admin := env.CreateUser(t, "admin", "pass")
	body := env.Get(t, "/apps/media/settings", admin.Auth).Status(200).ReadBody()
	for _, format := range []string{"OPML", "AntennaPod", "YouTube Takeout", "NewPipe"} {
		if !containsStr(body, format) {
			t.Errorf("settings page should mention %q", format)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && findStr(s, sub)
}

func findStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
