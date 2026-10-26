package calendar_test

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

// TestWebAttachments uploads a file in the event modal (PRG), lists and
// downloads it, and removes it; and downloads an attachment a client sent
// inline.
func TestWebAttachments(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)
	now := time.Now()
	ics := strings.Replace(genICS("att-uid", "With files", "", "", now.Add(time.Hour), now.Add(2*time.Hour), false, now),
		"END:VEVENT", "ATTACH;FMTTYPE=text/plain;FILENAME=hello.txt;VALUE=BINARY;ENCODING=BASE64:SGVsbG8=\r\nATTACH:https://example.com/spec.pdf\r\nEND:VEVENT", 1)
	ev := saveICS(t, env, db, cal, ics)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	detail := fmt.Sprintf("/apps/calendar/events/%d", ev.ID)

	page := env.Get(t, detail, cookie).Status(200).ReadBody()
	assert.Contains(t, page, ">hello.txt</a>")
	assert.Contains(t, page, `href="https://example.com/spec.pdf"`)
	inline := regexp.MustCompile(`href="(/apps/calendar/events/\d+/attachments/0/0)"`).FindStringSubmatch(page)
	require.True(t, inline != nil, page)
	got := env.Get(t, inline[1], cookie).Status(200)
	assert.Equal(t, "Hello", got.ReadBody())
	assert.Contains(t, got.Header.Get("Content-Disposition"), "hello.txt")

	// Upload: a redirect back to the event with a flash, and the file is a
	// managed attachment DAV clients see.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "notes.txt")
	require.NoError(t, err)
	fw.Write([]byte("meeting notes"))
	require.NoError(t, mw.Close())
	rq := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+detail+"/attachments", &body)
	rq.Header.Set("Content-Type", mw.FormDataContentType())
	resp := env.Do(t, rq, cookie, csrf, cogtest.StopRedirect).Status(303)
	assert.Equal(t, fmt.Sprintf("/apps/calendar/?event=%d", ev.ID), resp.Header.Get("Location"))

	stored, err := calendar.GetEvent(db, ev.ID)
	require.NoError(t, err)
	text := rendered(t, stored)
	assert.Contains(t, text, "FILENAME=notes.txt")
	assert.Contains(t, text, "MANAGED-ID=")
	assert.True(t, stored.Version > ev.Version)

	page = env.Get(t, detail, cookie).Status(200).ReadBody()
	link := regexp.MustCompile(`href="(/remote\.php/dav/attachments/alice/[^"]+)"[^>]*>notes\.txt<`).FindStringSubmatch(page)
	require.True(t, link != nil, page)
	env.Get(t, link[1], cookie).Status(200).Equals("meeting notes")
	id := regexp.MustCompile(`name="managed_id" value="([^"]+)"`).FindStringSubmatch(page)
	require.True(t, id != nil, page)

	// Remove it: gone from the event and from the server.
	env.PostForm(t, detail+"/attachments/remove", url.Values{"managed_id": {id[1]}}, cookie, csrf, cogtest.StopRedirect).Status(303)
	stored, err = calendar.GetEvent(db, ev.ID)
	require.NoError(t, err)
	assert.NotContains(t, rendered(t, stored), "notes.txt")
	env.Get(t, link[1], cookie).Status(410)
	// The client's own attachments stay.
	assert.Contains(t, strings.ReplaceAll(rendered(t, stored), "\r\n ", ""), "SGVsbG8=")
}
