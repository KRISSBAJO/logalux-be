package booking

import (
	"testing"
	"time"
)

func TestSlotsRespectHoursAndBusy(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	day := time.Date(2026, 10, 10, 0, 0, 0, 0, loc) // a Saturday
	hours := Hours{"sat": &[2]string{"08:00", "12:00"}}
	busy := []Busy{{Start: time.Date(2026, 10, 10, 9, 0, 0, 0, loc), End: time.Date(2026, 10, 10, 10, 0, 0, 0, loc)}}
	got := Slots(Params{Day: day, Loc: loc, Hours: hours, Busy: busy, DurationMin: 60, IntervalMin: 30})
	// 08:00 fits (ends 09:00, touching is fine), 08:30 clashes, 09:00/09:30 clash, 10:00 10:30 11:00 fit, 11:30 would end 12:30
	want := []string{"08:00", "10:00", "10:30", "11:00"}
	if len(got) != len(want) {
		t.Fatalf("got %d slots %v, want %v", len(got), got, want)
	}
	for i, w := range want {
		if got[i].In(loc).Format("15:04") != w {
			t.Errorf("slot %d = %s, want %s", i, got[i].In(loc).Format("15:04"), w)
		}
	}
}

func TestClosedDay(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, loc) // Sunday, closed
	if got := Slots(Params{Day: day, Loc: loc, Hours: Hours{"sun": nil}, DurationMin: 30}); got != nil {
		t.Fatalf("expected no slots on a closed day, got %v", got)
	}
}
