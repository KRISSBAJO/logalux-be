package httpapi

import (
	"strconv"
	"strings"
	"time"
)

// Reading someone's own calendar (the iCalendar format Google, Apple and Outlook all
// publish) to learn when they are busy. Only times are kept: never titles, places or guests.

type busySpan struct{ Start, End time.Time }

type icsResult struct {
	Busy   []busySpan
	Unread int // repeating events whose rule we do not understand; they are not blocked, and the person is told
	Own    int // events that came from LogaLuxe itself and are skipped
}

type icsEvent struct {
	start, end   time.Time
	allDay       bool
	rrule        string
	exdates      []time.Time
	uid          string
	cancelled    bool
	free         bool
	recurrenceID bool
	ok           bool
}

// unfoldICS joins continuation lines (a line starting with a space or tab continues the one before).
func unfoldICS(data string) []string {
	raw := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	var out []string
	for _, l := range raw {
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(out) > 0 {
			out[len(out)-1] += l[1:]
			continue
		}
		out = append(out, l)
	}
	return out
}

// icsTime reads a DTSTART-style value. params are the ";KEY=VALUE" parts before the colon.
func icsTime(params, value string, fallback *time.Location) (t time.Time, allDay, ok bool) {
	value = strings.TrimSpace(value)
	loc := fallback
	for _, p := range strings.Split(params, ";") {
		if strings.HasPrefix(strings.ToUpper(p), "TZID=") {
			name := strings.Trim(p[5:], `"`)
			if l, err := time.LoadLocation(name); err == nil {
				loc = l
			} else {
				return t, false, false // a time zone we cannot place: better to skip than to block the wrong hours
			}
		}
	}
	switch {
	case len(value) == 8:
		d, err := time.ParseInLocation("20060102", value, fallback)
		return d, true, err == nil
	case strings.HasSuffix(value, "Z"):
		d, err := time.Parse("20060102T150405Z", value)
		return d, false, err == nil
	default:
		d, err := time.ParseInLocation("20060102T150405", value, loc)
		return d, false, err == nil
	}
}

// icsDuration reads the simple durations calendars write: PT1H30M, P1D, PT45M, P1W.
func icsDuration(v string) (time.Duration, bool) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if !strings.HasPrefix(v, "P") {
		return 0, false
	}
	var d time.Duration
	num, inTime := "", false
	for _, c := range v[1:] {
		switch {
		case c == 'T':
			inTime = true
		case c >= '0' && c <= '9':
			num += string(c)
		default:
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, false
			}
			num = ""
			switch {
			case c == 'W':
				d += time.Duration(n) * 7 * 24 * time.Hour
			case c == 'D':
				d += time.Duration(n) * 24 * time.Hour
			case c == 'H' && inTime:
				d += time.Duration(n) * time.Hour
			case c == 'M' && inTime:
				d += time.Duration(n) * time.Minute
			case c == 'S' && inTime:
				d += time.Duration(n) * time.Second
			default:
				return 0, false
			}
		}
	}
	return d, d > 0
}

var icsWeekday = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

// expandRule lists the start times of a repeating event inside [from, to]. It understands daily and
// weekly rules with INTERVAL, BYDAY, COUNT and UNTIL, which covers almost every working calendar.
// Anything else returns ok=false and the event is left unblocked and counted.
func expandRule(start time.Time, rule string, from, to time.Time) (starts []time.Time, ok bool) {
	parts := map[string]string{}
	for _, kv := range strings.Split(strings.ToUpper(rule), ";") {
		if i := strings.Index(kv, "="); i > 0 {
			parts[kv[:i]] = kv[i+1:]
		}
	}
	for k := range parts {
		switch k {
		case "FREQ", "INTERVAL", "BYDAY", "COUNT", "UNTIL", "WKST":
		default:
			return nil, false
		}
	}
	interval := 1
	if v := parts["INTERVAL"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, false
		}
		interval = n
	}
	count := -1
	if v := parts["COUNT"]; v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, false
		}
		count = n
	}
	until := to
	if v := parts["UNTIL"]; v != "" {
		u, _, good := icsTime("", v, start.Location())
		if !good {
			return nil, false
		}
		if len(v) == 8 {
			u = u.Add(24*time.Hour - time.Second)
		}
		if u.Before(until) {
			until = u
		}
	}
	loc := start.Location()
	at := func(day time.Time) time.Time { // the same clock time on another day, so a 9:00 meeting stays at 9:00 across a clock change
		return time.Date(day.Year(), day.Month(), day.Day(), start.Hour(), start.Minute(), start.Second(), 0, loc)
	}
	made := 0
	add := func(t time.Time) bool { // false: stop
		if t.After(until) || (count >= 0 && made >= count) {
			return false
		}
		made++
		if !t.Before(from) {
			starts = append(starts, t)
		}
		return len(starts) < 2000
	}
	switch parts["FREQ"] {
	case "DAILY":
		if parts["BYDAY"] != "" {
			return nil, false
		}
		for day := start; ; day = day.AddDate(0, 0, interval) {
			if !add(at(day)) {
				break
			}
		}
	case "WEEKLY":
		days := []time.Weekday{start.Weekday()}
		if v := parts["BYDAY"]; v != "" {
			days = nil
			for _, d := range strings.Split(v, ",") {
				wd, known := icsWeekday[d]
				if !known {
					return nil, false // "1MO" and the like belong to monthly rules
				}
				days = append(days, wd)
			}
		}
		// Walk week by week from the week the event starts in (weeks begin on Monday unless WKST says Sunday).
		first := time.Monday
		if parts["WKST"] == "SU" {
			first = time.Sunday
		}
		weekStart := start.AddDate(0, 0, -((int(start.Weekday()) - int(first) + 7) % 7))
	weeks:
		for w := weekStart; ; w = w.AddDate(0, 0, 7*interval) {
			if at(w).After(until) {
				break
			}
			for i := 0; i < 7; i++ {
				day := w.AddDate(0, 0, i)
				match := false
				for _, d := range days {
					if day.Weekday() == d {
						match = true
					}
				}
				t := at(day)
				if !match || t.Before(start) {
					continue
				}
				if !add(t) {
					break weeks
				}
			}
		}
	default:
		return nil, false
	}
	return starts, true
}

// parseICS returns the busy times in a calendar file between from and to.
// Times with no zone of their own are read in fallback, the business's time zone.
func parseICS(data string, fallback *time.Location, from, to time.Time) icsResult {
	var res icsResult
	var ev *icsEvent
	var events []icsEvent
	var durProp string
	for _, line := range unfoldICS(data) {
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "BEGIN:VEVENT"):
			ev, durProp = &icsEvent{}, ""
			continue
		case strings.HasPrefix(upper, "END:VEVENT"):
			if ev != nil {
				if ev.ok && ev.end.IsZero() {
					if d, good := icsDuration(durProp); good {
						ev.end = ev.start.Add(d)
					} else if ev.allDay {
						ev.end = ev.start.AddDate(0, 0, 1)
					} else {
						ev.end = ev.start // an instant: nothing to block
					}
				}
				events = append(events, *ev)
			}
			ev = nil
			continue
		}
		if ev == nil {
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		head, value := line[:colon], line[colon+1:]
		name, params := head, ""
		if semi := strings.Index(head, ";"); semi >= 0 {
			name, params = head[:semi], head[semi+1:]
		}
		switch strings.ToUpper(name) {
		case "DTSTART":
			ev.start, ev.allDay, ev.ok = icsTime(params, value, fallback)
		case "DTEND":
			if t, _, good := icsTime(params, value, fallback); good {
				ev.end = t
			}
		case "DURATION":
			durProp = value
		case "RRULE":
			ev.rrule = value
		case "EXDATE":
			for _, v := range strings.Split(value, ",") {
				if t, _, good := icsTime(params, v, fallback); good {
					ev.exdates = append(ev.exdates, t)
				}
			}
		case "UID":
			ev.uid = strings.TrimSpace(value)
		case "STATUS":
			ev.cancelled = strings.EqualFold(strings.TrimSpace(value), "CANCELLED")
		case "TRANSP":
			ev.free = strings.EqualFold(strings.TrimSpace(value), "TRANSPARENT")
		case "RECURRENCE-ID":
			ev.recurrenceID = true
		}
	}
	for _, e := range events {
		switch {
		case strings.HasSuffix(strings.ToLower(e.uid), "@logaluxe"):
			res.Own++ // a LogaLuxe booking that came back through their calendar: already on the books
			continue
		case !e.ok || e.cancelled || e.free || !e.end.After(e.start):
			continue
		}
		length := e.end.Sub(e.start)
		if e.rrule == "" || e.recurrenceID {
			if e.end.After(from) && e.start.Before(to) {
				res.Busy = append(res.Busy, busySpan{e.start, e.end})
			}
			continue
		}
		starts, ok := expandRule(e.start, e.rrule, from.Add(-length), to)
		if !ok {
			res.Unread++
			continue
		}
	next:
		for _, s := range starts {
			for _, x := range e.exdates {
				if x.Equal(s) {
					continue next
				}
			}
			if s.Add(length).After(from) && s.Before(to) {
				res.Busy = append(res.Busy, busySpan{s, s.Add(length)})
			}
		}
	}
	return res
}
