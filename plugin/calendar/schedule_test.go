package calendar_test

import (
	"fmt"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

// TestWebScheduling invites, answers and cancels from the web UI: Cog users
// are scheduled by the server, others by email.
func TestWebScheduling(t *testing.T) {
	env, alice, cp, db, cal := calEnv(t)
	bob := env.CreateUser(t, "bob", "pass")
	for u, email := range map[*cogtest.User]string{alice: "alice@example.com", bob: "bob@example.com"} {
		require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{Email: &email}))
	}
	aliceAuth, aliceCSRF := sessionAuth(t, env, alice.ID)
	bobAuth, bobCSRF := sessionAuth(t, env, bob.ID)

	env.PostForm(t, "/apps/calendar/events", url.Values{
		"calendar_id":    {fmt.Sprint(cal.ID)},
		"summary":        {"Lunch"},
		"datetime_start": {"2026-11-02T12:00"},
		"datetime_end":   {"2026-11-02T13:00"},
		"tz":             {"UTC"},
		"attendee_email": {"bob@example.com", "carol@elsewhere.example"},
		"attendee_name":  {"Bob", "Carol"},
		"attendee_role":  {"REQ-PARTICIPANT", "REQ-PARTICIPANT"},
	}, aliceAuth, aliceCSRF, cogtest.StopRedirect).Status(303)

	events, err := calendar.ListEvents(db, cal.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(events))
	ev, err := calendar.GetEvent(db, events[0].ID)
	require.NoError(t, err)
	m := ev.Object.Master()
	require.True(t, m.Organizer != nil, "inviting makes alice the organizer")
	assert.Equal(t, "mailto:alice@example.com", m.Organizer.Address)
	assert.Equal(t, 1, len(env.Mailbox.ForRecipient("carol@elsewhere.example")))
	assert.Equal(t, 0, len(env.Mailbox.ForRecipient("bob@example.com")), "a Cog user is scheduled, not emailed")

	// Bob has it, and accepts it in the web UI.
	bobDB, err := cp.DB(bob.Username)
	require.NoError(t, err)
	bobCals, err := cp.Calendars(bob.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(bobCals))
	got, err := calendar.ListEvents(bobDB, bobCals[0].ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(got))
	assert.Equal(t, ev.UID, got[0].UID)
	env.Get(t, fmt.Sprintf("/apps/calendar/events/%d", got[0].ID), bobAuth).Status(200).Contains("Going?")
	env.Get(t, fmt.Sprintf("/apps/calendar/events/%d", ev.ID), aliceAuth).Status(200).NotContains("Going?")
	// Until he answers, it shows as an outline (D6).
	week := "/apps/calendar/events.json?start=2026-11-01T00:00:00Z&end=2026-11-08T00:00:00Z"
	env.Get(t, week, bobAuth).Status(200).Contains(`"cal-invite-pending"`)
	env.Get(t, week, aliceAuth).Status(200).NotContains(`"cal-invite-pending"`)
	env.PostForm(t, fmt.Sprintf("/apps/calendar/events/%d/respond", got[0].ID), url.Values{"status": {"ACCEPTED"}}, bobAuth, bobCSRF).Status(200)
	env.Get(t, week, bobAuth).Status(200).NotContains(`"cal-invite-pending"`)

	ev, err = calendar.GetEvent(db, ev.ID)
	require.NoError(t, err)
	for _, a := range ev.Object.Master().Attendees {
		if a.Address == "mailto:bob@example.com" {
			assert.Equal(t, "ACCEPTED", a.PartStat)
		}
	}

	// Alice deletes it: bob's copy is cancelled, carol is told.
	del := cogtest.NewRequest(t, "DELETE", env.Providers.Config.BaseURL+fmt.Sprintf("/apps/calendar/events/%d", ev.ID), nil)
	env.Do(t, del, aliceAuth, aliceCSRF).Status(200)
	mine, err := calendar.GetEvent(bobDB, got[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "CANCELLED", mine.Object.Master().Status)
	mail := env.Mailbox.ForRecipient("carol@elsewhere.example")
	require.Equal(t, 2, len(mail))
	assert.Equal(t, "Cancelled: Lunch", mail[0].Subject)
}
