package schedule

import (
	"strings"
	"testing"
	"time"
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadZone(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestLoadZone(t *testing.T) {
	for _, ok := range []string{"UTC", "Asia/Shanghai", "America/Los_Angeles", "America/Argentina/Buenos_Aires", "Etc/GMT+5", "Australia/Lord_Howe"} {
		if _, err := LoadZone(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus", "../etc/passwd", "Asia/Shanghai\n", "CST ", strings.Repeat("A", 70)} {
		_, err := LoadZone(bad)
		fe, ok := err.(*FieldError)
		if !ok || fe.Field != "timezone" {
			t.Errorf("%q: %v, want a time zone field error", bad, err)
		}
	}
}

func TestNormalize(t *testing.T) {
	got, err := Spec{Kind: KindWeekly, Time: "09:30", Days: []int{5, 1, 5, 3}, Every: 9, Date: "2026-01-01"}.normalize()
	if err != nil || got.Kind != KindWeekly || got.Time != "09:30" || len(got.Days) != 3 || got.Days[0] != 1 || got.Days[2] != 5 || got.Every != 0 || got.Date != "" {
		t.Fatalf("weekly = %+v, %v", got, err)
	}
	for _, bad := range []struct {
		spec  Spec
		field string
	}{
		{Spec{Kind: "cron"}, "kind"},
		{Spec{Kind: KindOnce, Date: "2026-02-30", Time: "09:00"}, "date"},
		{Spec{Kind: KindOnce, Date: "2026-02-03", Time: "24:00"}, "time"},
		{Spec{Kind: KindDaily, Time: "9:00"}, "time"},
		{Spec{Kind: KindWeekly, Time: "09:00"}, "days"},
		{Spec{Kind: KindWeekly, Time: "09:00", Days: []int{7}}, "days"},
		{Spec{Kind: KindInterval, Every: 5}, "every"},
		{Spec{Kind: KindInterval, Every: MaxEvery + 1}, "every"},
	} {
		_, err := bad.spec.normalize()
		if fe, ok := err.(*FieldError); !ok || fe.Field != bad.field {
			t.Errorf("%+v: %v, want field %s", bad.spec, err, bad.field)
		}
	}
}

// Daily times are wall-clock times: across DST the instant moves, the local
// time does not, and it is not a 24-hour interval.
func TestDailyIsWallClockAcrossDST(t *testing.T) {
	la := zone(t, "America/Los_Angeles")
	s := Spec{Kind: KindDaily, Time: "09:00"}
	o1, _ := s.next(utc("2026-03-07T18:00:00Z"), la, time.Time{}) // 10:00 PST Mar 7
	if o1.Key != "20260308t0900" || !o1.At.Equal(utc("2026-03-08T16:00:00Z")) {
		t.Fatalf("after spring forward: %+v", o1)
	}
	o0, _ := s.next(utc("2026-03-06T18:00:00Z"), la, time.Time{})
	if got := o1.At.Sub(o0.At); got != 23*time.Hour {
		t.Fatalf("a wall-clock day across spring forward is %v, want 23h", got)
	}
}

// A time that does not exist runs later by the gap; a repeated one runs once,
// at its first instance.
func TestDSTGapAndFold(t *testing.T) {
	la := zone(t, "America/Los_Angeles")
	gap := Spec{Kind: KindDaily, Time: "02:30"}
	o, ok := gap.next(utc("2026-03-08T08:00:00Z"), la, time.Time{}) // 00:00 PST
	if !ok || o.Key != "20260308t0230" || !o.At.Equal(utc("2026-03-08T10:30:00Z")) || o.At.In(la).Format("15:04 MST") != "03:30 PDT" {
		t.Fatalf("gap: %+v (%s)", o, o.At.In(la))
	}
	n, _ := gap.next(o.At, la, time.Time{})
	if n.Key != "20260309t0230" {
		t.Fatalf("after the gap day: %+v", n)
	}

	fold := Spec{Kind: KindDaily, Time: "01:30"}
	o, _ = fold.next(utc("2026-11-01T07:00:00Z"), la, time.Time{}) // 00:00 PDT
	if o.Key != "20261101t0130" || !o.At.Equal(utc("2026-11-01T08:30:00Z")) {
		t.Fatalf("fold: %+v", o)
	}
	// The second 01:30 (PST, 09:30Z) is not another occurrence.
	list, total, _ := fold.between(utc("2026-11-01T07:00:00Z"), utc("2026-11-01T12:00:00Z"), la, time.Time{}, 10)
	if total != 1 || list[0].Key != "20261101t0130" {
		t.Fatalf("fold occurrences: %d %+v", total, list)
	}
	n, _ = fold.next(o.At, la, time.Time{})
	if n.Key != "20261102t0130" {
		t.Fatalf("after the fold: %+v", n)
	}

	london := zone(t, "Europe/London")
	o, _ = Spec{Kind: KindDaily, Time: "01:15"}.next(utc("2026-03-28T12:00:00Z"), london, time.Time{})
	if o.Key != "20260329t0115" || o.At.In(london).Format("15:04") != "02:15" {
		t.Fatalf("london gap: %+v %s", o, o.At.In(london))
	}
	lh := zone(t, "Australia/Lord_Howe") // 30-minute DST
	o, ok = Spec{Kind: KindDaily, Time: "02:15"}.next(utc("2026-10-03T12:00:00Z"), lh, time.Time{})
	if !ok || o.Key != "20261004t0215" || o.At.In(lh).Format("15:04") != "02:45" {
		t.Fatalf("lord howe gap: %+v %s", o, o.At.In(lh))
	}
}

// Samoa skipped 30 December 2011: that day has no 09:00, and no two
// occurrences share an instant.
func TestSkippedDay(t *testing.T) {
	apia := zone(t, "Pacific/Apia")
	s := Spec{Kind: KindDaily, Time: "09:00"}
	list, total, _ := s.between(utc("2011-12-28T00:00:00Z"), utc("2012-01-02T00:00:00Z"), apia, time.Time{}, 10)
	seen := map[time.Time]bool{}
	for _, o := range list {
		if o.Key == "20111230t0900" {
			t.Fatalf("a skipped day ran: %+v", list)
		}
		if seen[o.At] {
			t.Fatalf("two occurrences at one instant: %+v", list)
		}
		seen[o.At] = true
	}
	if total != len(list) || total < 4 {
		t.Fatalf("occurrences: %d %+v", total, list)
	}
}

func TestWeeklyOnceInterval(t *testing.T) {
	sh := zone(t, "Asia/Shanghai")
	w := Spec{Kind: KindWeekly, Time: "18:30", Days: []int{1, 5}} // Mon, Fri
	o, _ := w.next(utc("2026-11-14T00:00:00Z"), sh, time.Time{})  // Sat 08:00 CST
	if o.Key != "20261116t1830" || o.At.In(sh).Weekday() != time.Monday {
		t.Fatalf("weekly: %+v", o)
	}
	o, _ = w.next(o.At, sh, time.Time{})
	if o.Key != "20261120t1830" {
		t.Fatalf("weekly next: %+v", o)
	}

	once := Spec{Kind: KindOnce, Date: "2026-11-20", Time: "09:00"}
	o, ok := once.next(utc("2026-11-14T00:00:00Z"), sh, time.Time{})
	if !ok || !o.At.Equal(utc("2026-11-20T01:00:00Z")) {
		t.Fatalf("once: %+v", o)
	}
	if _, ok := once.next(o.At, sh, time.Time{}); ok {
		t.Fatal("a once plan came due twice")
	}

	anchor := utc("2026-11-14T10:07:31Z")
	iv := Spec{Kind: KindInterval, Every: 120}
	o, _ = iv.next(anchor, sh, anchor)
	if !o.At.Equal(utc("2026-11-14T12:07:00Z")) || o.Key != "20261114t1207z" {
		t.Fatalf("interval: %+v", o)
	}
	// A week away counts without stepping through it, keeping the newest.
	list, total, _ := iv.between(anchor, anchor.Add(7*24*time.Hour+time.Minute), sh, anchor, 5)
	if total != 84 || len(list) != 5 || list[0].Key != "20261114t1207z" || !list[4].At.Equal(utc("2026-11-21T10:07:00Z")) {
		t.Fatalf("interval range: %d %+v", total, list)
	}
	// Elapsed time, not wall time: across DST an interval stays 2h apart.
	la := zone(t, "America/Los_Angeles")
	a := utc("2026-03-08T09:00:00Z")
	o1, _ := iv.next(a, la, a)
	o2, _ := iv.next(o1.At, la, a)
	if o2.At.Sub(o1.At) != 2*time.Hour {
		t.Fatalf("interval across DST: %v", o2.At.Sub(o1.At))
	}
}

func TestBetweenKeepsOldestAndNewest(t *testing.T) {
	s := Spec{Kind: KindDaily, Time: "09:00"}
	list, total, capped := s.between(utc("2026-01-01T00:00:00Z"), utc("2026-12-31T23:00:00Z"), time.UTC, time.Time{}, 4)
	if total != 365 || capped || len(list) != 4 || list[0].Key != "20260101t0900" || list[3].Key != "20261231t0900" || list[2].Key != "20261230t0900" {
		t.Fatalf("%d %v %+v", total, capped, list)
	}
}

func TestDescribe(t *testing.T) {
	for spec, want := range map[*Spec]string{
		{Kind: KindOnce, Date: "2026-11-20", Time: "09:00"}:  "once, on 2026-11-20 at 09:00",
		{Kind: KindDaily, Time: "07:05"}:                     "every day at 07:05",
		{Kind: KindWeekly, Time: "18:30", Days: []int{1, 5}}: "every week on Monday, Friday at 18:30",
		{Kind: KindInterval, Every: 15}:                      "every 15 minutes",
		{Kind: KindInterval, Every: 60}:                      "every hour",
		{Kind: KindInterval, Every: 180}:                     "every 3 hours",
		{Kind: KindInterval, Every: 2 * 24 * 60}:             "every 2 days",
	} {
		if got := spec.Describe(); got != want {
			t.Errorf("%+v: %q, want %q", *spec, got, want)
		}
	}
}
