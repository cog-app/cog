package calendar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Feeds, subscriptions and the birthday calendar, as the settings page
// manages them (DESIGN/dav-storage.md section 9). The DAV core does the
// work; these handlers are its forms, each a POST that redirects back to
// the settings page with a flash (PRG).

const settingsPath = "/apps/calendar/settings"

// feedView is a calendar's feeds as the settings page shows them: URLs, ""
// when there is none.
type feedView struct {
	Full, Busy string
}

// subscriptionView is a subscription as the settings page shows it.
type subscriptionView struct {
	Calendar
	Source      string
	LastSuccess string // "" before the first success
	NextPoll    string
	Failures    int
	LastError   string
	Skipped     int
}

// settingsView is what the settings page shows.
type settingsView struct {
	Calendars     []Calendar // the user's own, read-only ones included
	Feeds         map[int64]feedView
	Subscriptions []subscriptionView
	Birthdays     *Calendar // nil when off
	CanFeed       bool
}

func (p *Plugin) settingsView(userID string, cals []Calendar) (*settingsView, error) {
	v := &settingsView{Calendars: cals, Feeds: map[int64]feedView{}, CanFeed: p.dav.Links != nil}
	feeds, err := p.dav.FeedsOf(userID)
	if err != nil {
		return nil, err
	}
	for id, f := range feeds {
		var fv feedView
		if f.Full != nil {
			fv.Full = p.dav.FeedURL(f.Full.Token, dav.HomeCalendars)
		}
		if f.Busy != nil {
			fv.Busy = p.dav.FeedURL(f.Busy.Token, dav.HomeCalendars)
		}
		v.Feeds[int64(id)] = fv
	}
	subs, err := p.dav.Collections.Subscriptions(userID)
	if err != nil {
		return nil, err
	}
	for _, c := range cals {
		switch c.Kind {
		case dav.KindBirthdays:
			v.Birthdays = &c
		case dav.KindSubscription:
			sub := subs[dav.CollectionID(c.ID)]
			sv := subscriptionView{Calendar: c, Source: sub.URL, Failures: sub.Failures, LastError: sub.LastError, Skipped: sub.Skipped}
			if !sub.LastSuccess.IsZero() {
				sv.LastSuccess = sub.LastSuccess.In(c.Zone()).Format("2006-01-02 15:04")
			}
			if !sub.NextPoll.IsZero() {
				sv.NextPoll = sub.NextPoll.In(c.Zone()).Format("2006-01-02 15:04")
			}
			v.Subscriptions = append(v.Subscriptions, sv)
		}
	}
	return v, nil
}

// back redirects to the settings page with a message.
func back(w http.ResponseWriter, r *http.Request, f plugin.Flash) {
	plugin.SetFlash(w, f)
	http.Redirect(w, r, settingsPath, http.StatusSeeOther)
}

// failed logs an error of Cog's own and tells the user, without the detail.
func (h *Pages) failed(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.plugin.log.Error(what, "err", err)
	back(w, r, plugin.Flash{Kind: "error", Message: "Could not " + what + "."})
}

// Feed handles POST calendars/{id}/feed: action "renew" makes the feed (kind
// "full" or "busy") or gives it a new secret, "revoke" deletes it.
func (h *Pages) Feed(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	c, err := h.plugin.dav.Collections.ByID(dav.CollectionID(id))
	if errors.Is(err, dav.ErrNotFound) || (err == nil && (c.OwnerID != u.ID || !c.Has(dav.VEVENT))) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.failed(w, r, "change the feed", err)
		return
	}
	busy := r.FormValue("kind") == "busy"
	label := "feed"
	if busy {
		label = "free/busy feed"
	}
	switch r.FormValue("action") {
	case "renew":
		l, err := h.plugin.dav.RenewFeed(c, busy)
		if err != nil {
			h.failed(w, r, "make the feed", err)
			return
		}
		back(w, r, plugin.Flash{Kind: "success", Message: fmt.Sprintf("The %s of %s is at this address; an earlier one no longer works.", label, c.Title()), URL: h.plugin.dav.FeedURL(l.Token, dav.HomeCalendars)})
	case "revoke":
		if err := h.plugin.dav.RevokeFeed(c, busy); err != nil {
			h.failed(w, r, "revoke the feed", err)
			return
		}
		back(w, r, plugin.Flash{Kind: "success", Message: fmt.Sprintf("The %s of %s is revoked.", label, c.Title())})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// Subscribe handles POST subscriptions: a new read-only calendar filled
// from a remote feed, fetched at once so a URL that does not work is
// refused.
func (h *Pages) Subscribe(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	me, err := h.plugin.me(u.ID)
	if err != nil {
		h.failed(w, r, "subscribe", err)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Subscription"
	}
	c, res, err := h.plugin.dav.Subscribe(r.Context(), me, r.FormValue("url"), name, r.FormValue("color"))
	var fe *dav.FeedError
	switch {
	case errors.As(err, &fe):
		back(w, r, plugin.Flash{Kind: "error", Message: "Could not subscribe: " + fe.Error()})
		return
	case err != nil:
		h.failed(w, r, "subscribe", err)
		return
	}
	back(w, r, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Subscribed to %s: %d events.", c.Title(), res.Added)})
}

// RefreshSubscription handles POST subscriptions/{id}/refresh: fetch the
// feed now.
func (h *Pages) RefreshSubscription(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	c, err := h.plugin.dav.Collections.ByID(dav.CollectionID(id))
	if errors.Is(err, dav.ErrNotFound) || (err == nil && (c.OwnerID != u.ID || c.Kind != dav.KindSubscription)) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.failed(w, r, "refresh the subscription", err)
		return
	}
	res, err := h.plugin.dav.Refresh(r.Context(), u.Username, c)
	var fe *dav.FeedError
	switch {
	case errors.As(err, &fe):
		back(w, r, plugin.Flash{Kind: "error", Message: "Could not refresh " + c.Title() + ": " + fe.Error()})
	case err != nil:
		h.failed(w, r, "refresh the subscription", err)
	case res.NotModified:
		back(w, r, plugin.Flash{Kind: "success", Message: c.Title() + " has not changed."})
	default:
		back(w, r, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Refreshed %s: %d new, %d changed, %d removed.", c.Title(), res.Added, res.Updated, res.Removed)})
	}
}

// Birthdays handles POST birthdays: enable=1 makes the birthday calendar,
// anything else deletes it.
func (h *Pages) Birthdays(w http.ResponseWriter, r *http.Request) {
	u := plugin.UserFromContext(r.Context())
	me, err := h.plugin.me(u.ID)
	if err != nil {
		h.failed(w, r, "change the birthday calendar", err)
		return
	}
	if r.FormValue("enable") != "1" {
		if err := h.plugin.dav.DisableBirthdays(me); err != nil {
			h.failed(w, r, "remove the birthday calendar", err)
			return
		}
		back(w, r, plugin.Flash{Kind: "success", Message: "The birthday calendar is removed."})
		return
	}
	if _, err := h.plugin.dav.EnableBirthdays(me); err != nil {
		h.failed(w, r, "make the birthday calendar", err)
		return
	}
	back(w, r, plugin.Flash{Kind: "success", Message: "The birthday calendar shows your contacts' birthdays and anniversaries."})
}

// subscriptionLoop fetches the subscriptions that are due, every minute,
// and sweeps the birthday calendars every hour, for shares that reach
// users through groups whose members changed.
func (p *Plugin) subscriptionLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for tick := 1; ; tick++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.dav.PollDue(ctx); err != nil {
				p.log.Error("poll calendar subscriptions", "err", err)
			}
			if tick%60 == 0 {
				if err := p.dav.SweepBirthdays(); err != nil {
					p.log.Error("sweep birthday calendars", "err", err)
				}
			}
		}
	}
}

// webcalURL is a feed's address as calendar apps open it.
func webcalURL(u string) string {
	if rest, ok := strings.CutPrefix(u, "https://"); ok {
		return "webcal://" + rest
	}
	if rest, ok := strings.CutPrefix(u, "http://"); ok {
		return "webcal://" + rest
	}
	return u
}
