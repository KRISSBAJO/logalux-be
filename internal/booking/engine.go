// Package booking computes availability. It knows nothing about HTTP or payments:
// give it hours, existing bookings, and a duration, and it returns open start times.
package booking

import (
	"sort"
	"time"
)

// Hours is a location's weekly schedule: "mon" -> ["09:00","18:00"] or nil for closed.
type Hours map[string]*[2]string

// Busy is an existing booking's occupied range.
type Busy struct {
	Start, End time.Time
}

type Params struct {
	Day         time.Time // midnight in the location timezone
	Loc         *time.Location
	Hours       Hours
	Busy        []Busy
	DurationMin int       // total service time including processing
	BufferMin   int       // cleanup after
	IntervalMin int       // slot grid, default 30
	NotBefore   time.Time // lead time cutoff, zero to ignore
}

var dayKeys = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// Slots returns start times on the grid where the whole visit fits inside opening
// hours and overlaps nothing in Busy.
func Slots(p Params) []time.Time {
	if p.IntervalMin <= 0 {
		p.IntervalMin = 30
	}
	key := dayKeys[p.Day.In(p.Loc).Weekday()]
	rng, ok := p.Hours[key]
	if !ok || rng == nil {
		return nil
	}
	open, err1 := clock(p.Day, p.Loc, rng[0])
	close, err2 := clock(p.Day, p.Loc, rng[1])
	if err1 != nil || err2 != nil || !close.After(open) {
		return nil
	}
	need := time.Duration(p.DurationMin+p.BufferMin) * time.Minute
	step := time.Duration(p.IntervalMin) * time.Minute
	busy := append([]Busy(nil), p.Busy...)
	sort.Slice(busy, func(i, j int) bool { return busy[i].Start.Before(busy[j].Start) })

	var out []time.Time
	for t := open; !t.Add(need).After(close); t = t.Add(step) {
		if !p.NotBefore.IsZero() && t.Before(p.NotBefore) {
			continue
		}
		end := t.Add(need)
		clash := false
		for _, b := range busy {
			if b.Start.Before(end) && b.End.After(t) {
				clash = true
				break
			}
		}
		if !clash {
			out = append(out, t)
		}
	}
	return out
}

func clock(day time.Time, loc *time.Location, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	d := day.In(loc)
	return time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), 0, 0, loc), nil
}
