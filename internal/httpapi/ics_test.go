package httpapi

import (
	"strings"
	"testing"
	"time"
)

func icsFile(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func icsEv(lines ...string) string {
	return "BEGIN:VEVENT\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

func TestParseICS(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	lagos, _ := time.LoadLocation("Africa/Lagos")
	from := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC) // a Monday
	to := from.AddDate(0, 0, 28)
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, chicago) }

	t.Run("a plain event in UTC, one with a zone, and one with no zone", func(t *testing.T) {
		got := parseICS(icsFile(
			icsEv("UID:a", "DTSTART:20261006T150000Z", "DTEND:20261006T160000Z", "SUMMARY:Dentist"),
			icsEv("UID:b", "DTSTART;TZID=Africa/Lagos:20261007T090000", "DTEND;TZID=Africa/Lagos:20261007T093000"),
			icsEv("UID:c", "DTSTART:20261008T140000", "DURATION:PT1H30M"),
		), chicago, from, to)
		if len(got.Busy) != 3 {
			t.Fatalf("want 3 busy times, got %d", len(got.Busy))
		}
		if !got.Busy[0].Start.Equal(time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)) {
			t.Errorf("UTC start wrong: %v", got.Busy[0].Start)
		}
		if !got.Busy[1].Start.Equal(time.Date(2026, 10, 7, 9, 0, 0, 0, lagos)) {
			t.Errorf("zoned start wrong: %v", got.Busy[1].Start)
		}
		if !got.Busy[2].Start.Equal(day(8, 14, 0)) || !got.Busy[2].End.Equal(day(8, 15, 30)) {
			t.Errorf("floating time should be read in the business's zone, with its duration: %v to %v", got.Busy[2].Start, got.Busy[2].End)
		}
	})

	t.Run("free, cancelled, past and own events are left out", func(t *testing.T) {
		got := parseICS(icsFile(
			icsEv("UID:a", "DTSTART:20261006T150000Z", "DTEND:20261006T160000Z", "TRANSP:TRANSPARENT"),
			icsEv("UID:b", "DTSTART:20261006T170000Z", "DTEND:20261006T180000Z", "STATUS:CANCELLED"),
			icsEv("UID:c", "DTSTART:20260901T170000Z", "DTEND:20260901T180000Z"),
			icsEv("UID:1234@logaluxe", "DTSTART:20261009T170000Z", "DTEND:20261009T180000Z"),
			icsEv("UID:d", "DTSTART;VALUE=DATE:20261010", "DTEND;VALUE=DATE:20261011", "TRANSP:TRANSPARENT", "SUMMARY:Birthday"),
		), chicago, from, to)
		if len(got.Busy) != 0 || got.Own != 1 {
			t.Fatalf("want nothing busy and one own event, got %d busy, %d own", len(got.Busy), got.Own)
		}
	})

	t.Run("an all-day event that is marked busy blocks the whole local day", func(t *testing.T) {
		got := parseICS(icsFile(icsEv("UID:a", "DTSTART;VALUE=DATE:20261012", "DTEND;VALUE=DATE:20261013")), chicago, from, to)
		if len(got.Busy) != 1 || !got.Busy[0].Start.Equal(day(12, 0, 0)) || !got.Busy[0].End.Equal(day(13, 0, 0)) {
			t.Fatalf("want one whole day, got %+v", got.Busy)
		}
	})

	t.Run("weekly on two days, every week, with one date left out", func(t *testing.T) {
		got := parseICS(icsFile(icsEv("UID:a", "DTSTART;TZID=America/Chicago:20260901T090000", "DTEND;TZID=America/Chicago:20260901T100000",
			"RRULE:FREQ=WEEKLY;BYDAY=TU,TH", "EXDATE;TZID=America/Chicago:20261013T090000")), chicago, from, to)
		// Tuesdays and Thursdays from 5 Oct for four weeks: 6, 8, (13 left out), 15, 20, 22, 27, 29.
		want := []int{6, 8, 15, 20, 22, 27, 29}
		if len(got.Busy) != len(want) {
			t.Fatalf("want %d, got %d: %+v", len(want), len(got.Busy), got.Busy)
		}
		for i, d := range want {
			if !got.Busy[i].Start.Equal(day(d, 9, 0)) {
				t.Errorf("instance %d: want 9:00 on the %dth, got %v", i, d, got.Busy[i].Start)
			}
		}
	})

	t.Run("every two weeks, and a count that has run out", func(t *testing.T) {
		got := parseICS(icsFile(
			icsEv("UID:a", "DTSTART;TZID=America/Chicago:20260928T120000", "DTEND;TZID=America/Chicago:20260928T130000", "RRULE:FREQ=WEEKLY;INTERVAL=2"),
			icsEv("UID:b", "DTSTART;TZID=America/Chicago:20260901T120000", "DTEND;TZID=America/Chicago:20260901T130000", "RRULE:FREQ=DAILY;COUNT=5"),
		), chicago, from, to)
		// From Mon 28 Sep every other Monday: 12 Oct and 26 Oct fall in the window. The daily one ended on 5 Sep.
		if len(got.Busy) != 2 || !got.Busy[0].Start.Equal(day(12, 12, 0)) || !got.Busy[1].Start.Equal(day(26, 12, 0)) {
			t.Fatalf("want 12 and 26 Oct at noon, got %+v", got.Busy)
		}
	})

	t.Run("a daily rule keeps its clock time across the autumn clock change", func(t *testing.T) {
		nov := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
		got := parseICS(icsFile(icsEv("UID:a", "DTSTART;TZID=America/Chicago:20261030T090000", "DTEND;TZID=America/Chicago:20261030T093000", "RRULE:FREQ=DAILY;UNTIL=20261103T235959Z")), chicago, nov, nov.AddDate(0, 0, 10))
		if len(got.Busy) != 5 {
			t.Fatalf("want 5 days, got %d", len(got.Busy))
		}
		for _, b := range got.Busy {
			if b.Start.In(chicago).Hour() != 9 {
				t.Errorf("should stay at 9:00 local, got %v", b.Start.In(chicago))
			}
		}
	})

	t.Run("a rule we do not understand is counted, not guessed at", func(t *testing.T) {
		got := parseICS(icsFile(
			icsEv("UID:a", "DTSTART:20261006T150000Z", "DTEND:20261006T160000Z", "RRULE:FREQ=MONTHLY;BYDAY=1TU"),
			icsEv("UID:b", "DTSTART;TZID=Mars/Olympus:20261006T150000", "DTEND;TZID=Mars/Olympus:20261006T160000"),
		), chicago, from, to)
		if len(got.Busy) != 0 || got.Unread != 1 {
			t.Fatalf("want nothing blocked and one unread rule, got %d busy, %d unread", len(got.Busy), got.Unread)
		}
	})

	t.Run("long lines folded over several are joined", func(t *testing.T) {
		got := parseICS("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTART;TZID=America/Chi\r\n cago:20261006T0900\r\n 00\r\nDTEND;TZID=America/Chicago:20261006T100000\r\nEND:VEVENT\r\nEND:VCALENDAR", chicago, from, to)
		if len(got.Busy) != 1 || !got.Busy[0].Start.Equal(day(6, 9, 0)) {
			t.Fatalf("want one event at 9:00 on the 6th, got %+v", got.Busy)
		}
	})
}
