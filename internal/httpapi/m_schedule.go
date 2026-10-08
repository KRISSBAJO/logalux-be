package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"logaluxe/api/internal/booking"
)

// One place that answers "when can this be booked?". The public booking page
// and the merchant calendar both use it, so a roster change, approved time
// off or a blocked hour is respected everywhere.

type openSlot struct {
	Time    string `json:"time"`
	StartAt string `json:"starts_at"`
	StaffID string `json:"staff_id"`
	Staff   string `json:"staff"`
	// What the visit costs at this time with this person, after pricing rules.
	PriceCents int `json:"price_cents"`
}

var errUnknownServices = errors.New("unknown services")

// hoursOf decodes a stored weekly schedule. An empty or missing one returns nil.
func hoursOf(v any) booking.Hours {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var h booking.Hours
	if json.Unmarshal(b, &h) != nil || len(h) == 0 {
		return nil
	}
	return h
}

// staffBusy lists when a person cannot take a booking on a day: active
// bookings and blocked time. excludeBooking leaves one booking out, for moving it.
func (s *Server) staffBusy(ctx context.Context, staffID string, from, to time.Time, excludeBooking string) []booking.Busy {
	var busy []booking.Busy
	rs, _ := rows(ctx, s.pool, `select starts_at, ends_at from bookings where staff_id=$1 and status in ('confirmed','checked_in','in_progress') and starts_at < $3 and ends_at > $2 and ($4 = '' or id::text <> $4)
		union all select starts_at, ends_at from calendar_blocks where staff_id=$1 and starts_at < $3 and ends_at > $2`, staffID, from, to, excludeBooking)
	for _, b := range rs {
		busy = append(busy, booking.Busy{Start: b["starts_at"].(time.Time), End: b["ends_at"].(time.Time)})
	}
	return busy
}

func (s *Server) openSlots(ctx context.Context, businessID string, loc *time.Location, day time.Time, serviceIDs []string, staffFilter string, notBefore time.Time, excludeBooking string) ([]openSlot, int, error) {
	var durMin, bufMin int
	if err := s.pool.QueryRow(ctx, `select coalesce(sum(duration_min+processing_min),0), coalesce(max(buffer_min),0) from services where business_id=$1 and id = any($2::uuid[])`, businessID, serviceIDs).Scan(&durMin, &bufMin); err != nil || durMin == 0 {
		return nil, 0, errUnknownServices
	}
	var locHoursRaw any
	_ = s.pool.QueryRow(ctx, `select hours from locations where business_id=$1 and is_primary`, businessID).Scan(&locHoursRaw)
	locHours := hoursOf(locHoursRaw)

	// People who can do every requested service and are not on approved time off that day.
	staffRows, err := rows(ctx, s.pool, `
		select st.id, st.name, st.hours, st.breaks from staff st
		where st.business_id=$1 and st.bookable and not st.archived and st.pay_type <> 'renter'
		  and ($2='' or $2='any' or st.id::text=$2)
		  and not exists (select 1 from unnest($3::uuid[]) sid where not exists (select 1 from staff_services ss where ss.staff_id=st.id and ss.service_id=sid))
		  and not exists (select 1 from time_off t where t.staff_id=st.id and t.status='approved' and $4::date between t.starts_on and t.ends_on)
		order by st.role='owner' desc, st.name`, businessID, staffFilter, serviceIDs, day.Format("2006-01-02"))
	if err != nil {
		return nil, 0, err
	}
	out := []openSlot{}
	dayEnd := day.Add(24 * time.Hour)
	rooms := s.resourcesFor(ctx, businessID, serviceIDs, day, dayEnd, excludeBooking)
	prices := s.loadPricing(ctx, businessID)
	visit := time.Duration(durMin+bufMin) * time.Minute
	for _, st := range staffRows {
		id := fmt.Sprint(st["id"])
		hours := hoursOf(st["hours"]) // a person's own roster wins over the location's hours
		if hours == nil {
			hours = locHours
		}
		if hours == nil {
			continue
		}
		busy := append(s.staffBusy(ctx, id, day, dayEnd, excludeBooking), breaksOn(st["breaks"], day, loc)...)
		for _, t := range booking.Slots(booking.Params{Day: day, Loc: loc, Hours: hours, Busy: busy, DurationMin: durMin, BufferMin: bufMin, IntervalMin: 30, NotBefore: notBefore}) {
			if clash(rooms, t, t.Add(visit)) != "" {
				continue // the person is free but the chair or room is not
			}
			out = append(out, openSlot{Time: t.In(loc).Format("15:04"), StartAt: t.Format(time.RFC3339), StaffID: id, Staff: fmt.Sprint(st["name"]), PriceCents: prices.total(serviceIDs, id, t.In(loc))})
		}
	}
	return out, durMin, nil
}

// visitLength returns how long a set of services takes, with clean-up time.
func (s *Server) visitLength(ctx context.Context, businessID string, serviceIDs []string) (minutes int, err error) {
	var dur, buf int
	if err := s.pool.QueryRow(ctx, `select coalesce(sum(duration_min+processing_min),0), coalesce(max(buffer_min),0) from services where business_id=$1 and id = any($2::uuid[])`, businessID, serviceIDs).Scan(&dur, &buf); err != nil || dur == 0 {
		return 0, errUnknownServices
	}
	return dur + buf, nil
}
