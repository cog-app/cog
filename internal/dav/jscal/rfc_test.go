package jscal

import (
	"encoding/json"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/dav/jsobj"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// The examples of RFC 8984 section 6, with the "..." placeholders filled
// in. 6.3 (a Group) is not a type Cog stores.
var rfcExamples = map[string]string{
	"6.1 simple event": `{
  "@type": "Event",
  "uid": "a8df6573-0474-496d-8496-033ad45d7fea",
  "updated": "2020-01-02T18:23:04Z",
  "title": "Some event",
  "start": "2020-01-15T13:00:00",
  "timeZone": "America/New_York",
  "duration": "PT1H"
}`,
	"6.2 simple task": `{
  "@type": "Task",
  "uid": "2a358cee-6489-4f14-a57f-c104db4dc2f2",
  "updated": "2020-01-09T14:32:01Z",
  "title": "Do something"
}`,
	"6.4 all-day event": `{
  "@type": "Event",
  "uid": "a8df6573-0474-496d-8496-033ad45d7fea",
  "title": "April Fool's Day",
  "showWithoutTime": true,
  "start": "1900-04-01T00:00:00",
  "duration": "P1D",
  "recurrenceRules": [{
    "@type": "RecurrenceRule",
    "frequency": "yearly"
  }]
}`,
	"6.5 task with a due date": `{
  "@type": "Task",
  "uid": "2a358cee-6489-4f14-a57f-c104db4dc2f2",
  "title": "Buy groceries",
  "due": "2020-01-19T18:00:00",
  "timeZone": "Europe/Vienna",
  "estimatedDuration": "PT1H"
}`,
	"6.6 event with end time zone": `{
  "@type": "Event",
  "uid": "a8df6573-0474-496d-8496-033ad45d7fea",
  "title": "Flight XY51 to Tokyo",
  "start": "2020-04-01T09:00:00",
  "timeZone": "Europe/Berlin",
  "duration": "PT10H30M",
  "locations": {
    "418d0b9b-b656-4b3c-909f-5b149ca779c9": {
      "@type": "Location",
      "relativeTo": "start",
      "name": "Frankfurt Airport (FRA)"
    },
    "c2c7ac67-dc13-411e-a7d4-0780fb61fb08": {
      "@type": "Location",
      "relativeTo": "end",
      "name": "Narita International Airport (NRT)",
      "timeZone": "Asia/Tokyo"
    }
  }
}`,
	"6.7 floating-time event": `{
  "@type": "Event",
  "uid": "a8df6573-0474-496d-8496-033ad45d7fea",
  "title": "Yoga",
  "start": "2020-01-01T07:00:00",
  "duration": "PT30M",
  "recurrenceRules": [{
    "@type": "RecurrenceRule",
    "frequency": "daily"
  }]
}`,
	"6.8 multiple locations and localization": `{
  "@type": "Event",
  "uid": "a8df6573-0474-496d-8496-033ad45d7fea",
  "title": "Live from Music Bowl: The Band",
  "description": "Go see the biggest music event ever!",
  "locale": "en",
  "start": "2020-07-04T17:00:00",
  "timeZone": "America/New_York",
  "duration": "PT3H",
  "locations": {
    "c0503d30-8c50-4372-87b5-7657e8e0fedd": {
      "@type": "Location",
      "name": "The Music Bowl",
      "description": "Music Bowl, Central Park, New York",
      "coordinates": "geo:40.7829,-73.9654"
    }
  },
  "virtualLocations": {
    "vloc1": {
      "@type": "VirtualLocation",
      "name": "Free live Stream from Music Bowl",
      "uri": "https://stream.example.com/the_band_2020"
    }
  },
  "localizations": {
    "de": {
      "title": "Live von der Music Bowl: The Band!",
      "description": "Schau dir das größte Musikereignis an!",
      "virtualLocations/vloc1/name": "Gratis Live-Stream aus der Music Bowl"
    }
  }
}`,
	"6.9 recurring event with overrides": `{
  "@type": "Event",
  "uid": "a8df6573-0474-496d-8496-033ad45d7fea",
  "title": "Calculus I",
  "start": "2020-01-08T09:00:00",
  "timeZone": "Europe/London",
  "duration": "PT1H30M",
  "locations": {
    "2a358cee-6489-4f14-a57f-c104db4dc2f1": {
      "@type": "Location",
      "title": "Math lab room 1",
      "description": "Math Lab I, Department of Mathematics"
    }
  },
  "recurrenceRules": [{
    "@type": "RecurrenceRule",
    "frequency": "weekly",
    "until": "2020-06-24T09:00:00"
  }],
  "recurrenceOverrides": {
    "2020-01-07T14:00:00": {
      "title": "Introduction to Calculus I (optional)"
    },
    "2020-04-01T09:00:00": {
      "excluded": true
    },
    "2020-06-25T09:00:00": {
      "title": "Calculus I Exam",
      "start": "2020-06-25T10:00:00",
      "duration": "PT2H",
      "locations": {
        "2a358cee-6489-4f14-a57f-c104db4dc2f1": {
          "@type": "Location",
          "title": "Big Auditorium",
          "description": "Big Auditorium, Other Road"
        }
      }
    }
  }
}`,
	"6.10 recurring event with participants": `{
  "@type": "Event",
  "uid": "a8df6573-0474-496d-8496-033ad45d7fea",
  "title": "FooBar team meeting",
  "start": "2020-01-08T09:00:00",
  "timeZone": "Africa/Johannesburg",
  "duration": "PT1H",
  "virtualLocations": {
    "0": {
      "@type": "VirtualLocation",
      "name": "ChatMe meeting room",
      "uri": "https://chatme.example.com?id=1234567&pw=a8a24627b63d"
    }
  },
  "recurrenceRules": [{
    "@type": "RecurrenceRule",
    "frequency": "weekly"
  }],
  "replyTo": {
    "imip": "mailto:f245f875-7f63-4a5e-a2c8@schedule.example.com"
  },
  "participants": {
    "dG9tQGZvb2Jhci5xlLmNvbQ": {
      "@type": "Participant",
      "name": "Tom Tool",
      "email": "tom@foobar.example.com",
      "sendTo": {
        "imip": "mailto:tom@calendar.example.com"
      },
      "participationStatus": "accepted",
      "roles": {
        "attendee": true
      }
    },
    "em9lQGZvb2GFtcGxlLmNvbQ": {
      "@type": "Participant",
      "name": "Zoe Zelda",
      "email": "zoe@foobar.example.com",
      "sendTo": {
        "imip": "mailto:zoe@foobar.example.com"
      },
      "participationStatus": "accepted",
      "roles": {
        "owner": true,
        "attendee": true,
        "chair": true
      }
    }
  },
  "recurrenceOverrides": {
    "2020-03-04T09:00:00": {
      "participants/dG9tQGZvb2Jhci5xlLmNvbQ/participationStatus": "declined"
    }
  }
}`,
}

// withoutVendor removes Cog's own properties, which a client that sent the
// object did not.
func withoutVendor(v any) any {
	switch t := v.(type) {
	case jsobj.Object:
		out := jsobj.Object{}
		for k, x := range t {
			if !strings.HasPrefix(k, jsobj.Vendor) {
				out[k] = withoutVendor(x)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = withoutVendor(x)
		}
		return out
	}
	return v
}

func pretty(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

// TestRFCExamples reads each example, writes it back, and checks that what
// Cog writes is what the example said (Cog's own properties aside), that
// reading that again gives the same model, and that the iCalendar written
// from it says what the example means.
func TestRFCExamples(t *testing.T) {
	for name, text := range rfcExamples {
		t.Run(name, func(t *testing.T) {
			in, err := jsobj.ParseObject([]byte(text))
			require.NoError(t, err)
			o, err := Decode(in)
			require.NoError(t, err)
			out, err := Encode(o)
			require.NoError(t, err)
			again, err := Decode(out)
			require.NoError(t, err)
			require.Equal(t, pretty(o), pretty(again), "decode(encode(decode(x))) != decode(x)")

			got := withoutVendor(out).(jsobj.Object)
			want := jsobj.Clone(in).(jsobj.Object)
			// Override patches are written whole-property; compare them as
			// the occurrences they make.
			if _, ok := want[keyOverrides]; ok {
				assert.Equal(t, pretty(occurrences(t, want)), pretty(occurrences(t, got)))
				delete(want, keyOverrides)
				delete(got, keyOverrides)
			}
			assert.Equal(t, pretty(want), pretty(got))
		})
	}
}

// occurrences expands recurrenceOverrides into the objects each patch
// makes of the master, without Cog's properties.
func occurrences(t *testing.T, o jsobj.Object) jsobj.Object {
	t.Helper()
	base := jsobj.Clone(o).(jsobj.Object)
	for _, k := range []string{keyOverrides, "recurrenceRules", "uid", "@type"} {
		delete(base, k)
	}
	out := jsobj.Object{}
	entries, _ := o[keyOverrides].(jsobj.Object)
	for k, e := range entries {
		full := jsobj.Clone(base).(jsobj.Object)
		full["start"] = k
		patch := jsobj.Object{}
		for pk, pv := range e.(jsobj.Object) {
			if !strings.HasPrefix(pk, jsobj.Vendor) {
				patch[pk] = pv
			}
		}
		require.NoError(t, ApplyPatch(full, patch))
		out[k] = withoutVendor(full)
	}
	return out
}

// TestRFCExamplesInICalendar checks what the examples mean once they are
// iCalendar.
func TestRFCExamplesInICalendar(t *testing.T) {
	read := func(name string) *ical.Object {
		in, err := jsobj.ParseObject([]byte(rfcExamples[name]))
		require.NoError(t, err)
		o, err := Decode(in)
		require.NoError(t, err)
		return o
	}

	o := read("6.1 simple event")
	m := o.Master()
	assert.Equal(t, ical.DateTime{Local: "20200115T130000", TZID: "America/New_York"}, m.Start)
	assert.Equal(t, ical.DateTime{Local: "20200115T140000", TZID: "America/New_York"}, m.End)
	assert.Equal(t, ical.DateTime{Local: "20200102T182304", TZID: "UTC"}, m.LastModified)

	o = read("6.4 all-day event")
	m = o.Master()
	assert.Equal(t, ical.DateTime{Local: "19000401", IsDate: true}, m.Start)
	assert.Equal(t, ical.DateTime{Local: "19000402", IsDate: true}, m.End)
	assert.Equal(t, "FREQ=YEARLY", m.RRule)

	o = read("6.5 task with a due date")
	m = o.Master()
	assert.Equal(t, ical.VTODO, m.Kind)
	assert.Equal(t, ical.DateTime{Local: "20200119T180000", TZID: "Europe/Vienna"}, m.Due)
	assert.Equal(t, "PT1H", m.Duration)

	o = read("6.6 event with end time zone")
	m = o.Master()
	assert.Equal(t, "Frankfurt Airport (FRA)", m.Location)
	assert.Equal(t, ical.DateTime{Local: "20200401T193000", TZID: "Europe/Berlin"}, m.End)

	o = read("6.7 floating-time event")
	assert.Equal(t, ical.DateTime{Local: "20200101T070000"}, o.Master().Start)

	o = read("6.9 recurring event with overrides")
	m = o.Master()
	assert.Equal(t, "FREQ=WEEKLY;UNTIL=20200624T080000Z", m.RRule)
	assert.Equal(t, []ical.DateValue{{DateTime: ical.DateTime{Local: "20200401T090000", TZID: "Europe/London"}}}, m.ExDates)
	require.Equal(t, 3, len(o.Components))
	exam := o.Components[2]
	assert.Equal(t, "Calculus I Exam", exam.Summary)
	assert.Equal(t, ical.DateTime{Local: "20200625T090000", TZID: "Europe/London"}, exam.RecurrenceID)
	assert.Equal(t, ical.DateTime{Local: "20200625T100000", TZID: "Europe/London"}, exam.Start)
	assert.Equal(t, ical.DateTime{Local: "20200625T120000", TZID: "Europe/London"}, exam.End)

	o = read("6.10 recurring event with participants")
	m = o.Master()
	assert.Equal(t, "mailto:f245f875-7f63-4a5e-a2c8@schedule.example.com", m.Organizer.Address)
	require.Equal(t, 2, len(m.Attendees))
	tom := m.Attendees[0]
	assert.Equal(t, "Tom Tool", tom.CN)
	assert.Equal(t, "mailto:tom@calendar.example.com", tom.Address)
	assert.Equal(t, "ACCEPTED", tom.PartStat)
	assert.Equal(t, "CHAIR", m.Attendees[1].Role)
	require.Equal(t, 2, len(o.Components))
	ov := o.Components[1]
	assert.Equal(t, ical.DateTime{Local: "20200304T090000", TZID: "Africa/Johannesburg"}, ov.RecurrenceID)
	assert.Equal(t, "DECLINED", ov.Attendees[0].PartStat)
	assert.Equal(t, "ACCEPTED", ov.Attendees[1].PartStat)
}

// TestUnknownPropertiesSurviveICalendar checks that what a client sent and
// Cog does not model is still there after the object has been through
// iCalendar text, as a DAV client would see it and write it back.
func TestUnknownPropertiesSurviveICalendar(t *testing.T) {
	in, err := jsobj.ParseObject([]byte(rfcExamples["6.8 multiple locations and localization"]))
	require.NoError(t, err)
	o, err := Decode(in)
	require.NoError(t, err)
	text := encodeICS(t, o)
	assert.Contains(t, text, "JSPROP;JSPTR=/localizations:")
	back := decodeICS(t, text, ical.VEVENT)
	back.SortMaster()
	out, err := Encode(back)
	require.NoError(t, err)
	// Written as iCalendar, it gained Cog's PRODID.
	assert.Equal(t, ical.ProdID, out["prodId"])
	delete(out, "prodId")
	assert.Equal(t, pretty(in), pretty(withoutVendor(out)))
}
