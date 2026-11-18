// Package schedule keeps a yesman's scheduled tasks: plans the user saves in
// Tasks, each a request for its lead that comes due once, every day, every
// week or every so often. While the app runs, a long-lived service claims
// each due occurrence once, adds a board task for that run and sends the
// lead the request through the agent manager's receipt path. Occurrences the
// app was not running (or awake) for are recorded as missed, never replayed
// by themselves; the user can catch up by running a plan now.
//
// Plans are configuration (.yesman/schedules/plans.json); every run, missed
// range and skipped occurrence is a record of its own
// (.yesman/schedules/runs/<id>.json) whose exclusive creation is the claim.
// What a run achieved is its board task's status, which the lead and its
// workers keep; the scheduler records only scheduling and delivery facts.
package schedule

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	// Plans name IANA time zones; the embedded database resolves them the
	// same way on macOS, Windows and minimal Linux systems.
	_ "time/tzdata"
)

// Schedule kinds.
const (
	KindOnce     = "once"
	KindDaily    = "daily"
	KindWeekly   = "weekly"
	KindInterval = "interval"
)

// Bounds of an interval, in minutes.
const (
	MinEvery = 15
	MaxEvery = 7 * 24 * 60
)

// OnTime is how late an occurrence may be seen and still run. Later than
// that (the app was closed or asleep, or the clock jumped) it is missed.
const OnTime = 10 * time.Minute

// Spec says when a plan comes due. Date and time are wall-clock values in
// the plan's time zone; an interval is elapsed time.
type Spec struct {
	Kind  string `json:"kind"`
	Date  string `json:"date,omitempty"`  // once: YYYY-MM-DD
	Time  string `json:"time,omitempty"`  // once, daily, weekly: HH:MM, 24-hour
	Days  []int  `json:"days,omitempty"`  // weekly: 0 Sunday … 6 Saturday
	Every int    `json:"every,omitempty"` // interval: minutes
}

// FieldError is a validation error of one field of a plan, for the form.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *FieldError) Error() string { return e.Message }

func fieldError(field, format string, args ...any) error {
	return &FieldError{Field: field, Message: fmt.Sprintf(format, args...)}
}

var (
	timeRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
	dateRE = regexp.MustCompile(`^([0-9]{4})-([0-9]{2})-([0-9]{2})$`)
	zoneRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-]*(/[A-Za-z0-9_+\-]+){0,3}$`)
)

// LoadZone validates and loads an IANA time zone name, such as
// Asia/Shanghai or UTC. "Local" and abbreviations are refused: a plan keeps
// its own zone when the system's changes.
func LoadZone(name string) (*time.Location, error) {
	if name == "" || len(name) > 64 || name == "Local" || !zoneRE.MatchString(name) {
		return nil, fieldError("timezone", "Choose a time zone such as Asia/Shanghai or America/New_York.")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fieldError("timezone", "%s is not a time zone yesman knows.", name)
	}
	return loc, nil
}

func clock(s string) (hour, minute int, ok bool) {
	m := timeRE.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, false
	}
	hour, _ = strconv.Atoi(m[1])
	minute, _ = strconv.Atoi(m[2])
	return hour, minute, true
}

func calendarDate(s string) (y int, mo time.Month, d int, ok bool) {
	m := dateRE.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, 0, false
	}
	y, _ = strconv.Atoi(m[1])
	month, _ := strconv.Atoi(m[2])
	d, _ = strconv.Atoi(m[3])
	t := time.Date(y, time.Month(month), d, 12, 0, 0, 0, time.UTC)
	if y < 2000 || y > 2200 || t.Year() != y || t.Month() != time.Month(month) || t.Day() != d {
		return 0, 0, 0, false
	}
	return y, time.Month(month), d, true
}

// normalize checks a spec and returns it in its canonical form: weekdays
// sorted and unique, fields of other kinds cleared.
func (s Spec) normalize() (Spec, error) {
	switch s.Kind {
	case KindOnce:
		if _, _, _, ok := calendarDate(s.Date); !ok {
			return s, fieldError("date", "Choose a date.")
		}
		if _, _, ok := clock(s.Time); !ok {
			return s, fieldError("time", "Choose a time.")
		}
		return Spec{Kind: KindOnce, Date: s.Date, Time: s.Time}, nil
	case KindDaily:
		if _, _, ok := clock(s.Time); !ok {
			return s, fieldError("time", "Choose a time.")
		}
		return Spec{Kind: KindDaily, Time: s.Time}, nil
	case KindWeekly:
		if _, _, ok := clock(s.Time); !ok {
			return s, fieldError("time", "Choose a time.")
		}
		var days []int
		for _, d := range s.Days {
			if d < 0 || d > 6 {
				return s, fieldError("days", "Choose days of the week.")
			}
			if !slices.Contains(days, d) {
				days = append(days, d)
			}
		}
		if len(days) == 0 {
			return s, fieldError("days", "Choose at least one day of the week.")
		}
		slices.Sort(days)
		return Spec{Kind: KindWeekly, Time: s.Time, Days: days}, nil
	case KindInterval:
		if s.Every < MinEvery || s.Every > MaxEvery {
			return s, fieldError("every", "Run it every 15 minutes to every 7 days.")
		}
		return Spec{Kind: KindInterval, Every: s.Every}, nil
	}
	return s, fieldError("kind", "Choose when it runs.")
}

// Occurrence is one time a plan comes due. Key names it in the plan's own
// terms (its local date and time, or an interval's instant) so that it runs
// at most once, whatever the clock or DST do.
type Occurrence struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}

func localKey(y int, mo time.Month, d, hour, minute int) string {
	return fmt.Sprintf("%04d%02d%02dt%02d%02d", y, mo, d, hour, minute)
}

func intervalKey(at time.Time) string { return at.UTC().Format("20060102t1504") + "z" }

// resolve finds the instant a wall-clock time on a date has in a zone.
// A time that occurs twice, when clocks go back, resolves to the first; a
// time that does not exist, when clocks go forward, resolves to the same
// time later by the length of the gap (02:30 becomes 03:30), as java.time
// does. If that moves it to another day, the day has no such time: ok false.
func resolve(y int, mo time.Month, d, hour, minute int, loc *time.Location) (time.Time, bool) {
	naive := time.Date(y, mo, d, hour, minute, 0, 0, time.UTC)
	wall := func(t time.Time) time.Time {
		l := t.In(loc)
		return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
	}
	var offsets []int
	for _, probe := range []time.Duration{-48, -24, -12, 0, 12, 24, 48} {
		_, off := naive.Add(probe * time.Hour).In(loc).Zone()
		if !slices.Contains(offsets, off) {
			offsets = append(offsets, off)
		}
	}
	var exact, later time.Time
	for _, off := range offsets {
		c := naive.Add(-time.Duration(off) * time.Second)
		switch w := wall(c); {
		case w.Equal(naive):
			if exact.IsZero() || c.Before(exact) {
				exact = c
			}
		case w.After(naive):
			if later.IsZero() || w.Before(wall(later)) {
				later = c
			}
		}
	}
	if !exact.IsZero() {
		return exact, true
	}
	if later.IsZero() {
		return time.Time{}, false
	}
	if l := later.In(loc); l.Year() != y || l.Month() != mo || l.Day() != d {
		return time.Time{}, false
	}
	return later, true
}

// next returns the plan's first occurrence strictly after a time. anchor is
// where an interval counts from.
func (s Spec) next(after time.Time, loc *time.Location, anchor time.Time) (Occurrence, bool) {
	switch s.Kind {
	case KindOnce:
		y, mo, d, ok := calendarDate(s.Date)
		hour, minute, ok2 := clock(s.Time)
		if !ok || !ok2 {
			return Occurrence{}, false
		}
		at, ok := resolve(y, mo, d, hour, minute, loc)
		if !ok || !at.After(after) {
			return Occurrence{}, false
		}
		return Occurrence{Key: localKey(y, mo, d, hour, minute), At: at.UTC()}, true
	case KindDaily, KindWeekly:
		hour, minute, ok := clock(s.Time)
		if !ok {
			return Occurrence{}, false
		}
		start := after.In(loc)
		for i := -1; i <= 400; i++ {
			day := time.Date(start.Year(), start.Month(), start.Day()+i, 12, 0, 0, 0, time.UTC)
			if s.Kind == KindWeekly && !slices.Contains(s.Days, int(day.Weekday())) {
				continue
			}
			at, ok := resolve(day.Year(), day.Month(), day.Day(), hour, minute, loc)
			if ok && at.After(after) {
				return Occurrence{Key: localKey(day.Year(), day.Month(), day.Day(), hour, minute), At: at.UTC()}, true
			}
		}
		return Occurrence{}, false
	case KindInterval:
		if s.Every < MinEvery || anchor.IsZero() {
			return Occurrence{}, false
		}
		every := time.Duration(s.Every) * time.Minute
		base := anchor.UTC().Truncate(time.Minute)
		k := int64(1)
		if after.After(base) {
			k = int64(after.Sub(base)/every) + 1
		}
		at := base.Add(time.Duration(k) * every)
		for !at.After(after) {
			at = at.Add(every)
		}
		return Occurrence{Key: intervalKey(at), At: at}, true
	}
	return Occurrence{}, false
}

// between lists the occurrences after from and at or before to: the oldest,
// then the newest up to limit in all, oldest first. total counts them all;
// capped reports that counting stopped early.
func (s Spec) between(from, to time.Time, loc *time.Location, anchor time.Time, limit int) (list []Occurrence, total int, capped bool) {
	if s.Kind == KindInterval {
		// Count arithmetically: a long absence must not take a long loop.
		first, ok := s.next(from, loc, anchor)
		if !ok || first.At.After(to) {
			return nil, 0, false
		}
		every := time.Duration(s.Every) * time.Minute
		total = int(to.Sub(first.At)/every) + 1
		list = append(list, first)
		if total > 1 {
			start := first.At.Add(time.Duration(max(1, total-limit+1)) * every)
			for at := start; !at.After(to) && len(list) < limit; at = at.Add(every) {
				list = append(list, Occurrence{Key: intervalKey(at), At: at})
			}
		}
		return list, total, false
	}
	at := from
	for {
		o, ok := s.next(at, loc, anchor)
		if !ok || o.At.After(to) {
			return list, total, capped
		}
		total++
		if len(list) < limit {
			list = append(list, o)
		} else {
			// Keep the oldest, and slide the rest so the newest stay.
			list = append(list[:1], list[2:]...)
			list = append(list, o)
		}
		if total >= 100000 {
			return list, total, true
		}
		at = o.At
	}
}

var weekdayNames = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// Describe says when a plan runs, in English, for the lead and the board.
func (s Spec) Describe() string {
	switch s.Kind {
	case KindOnce:
		return fmt.Sprintf("once, on %s at %s", s.Date, s.Time)
	case KindDaily:
		return "every day at " + s.Time
	case KindWeekly:
		names := make([]string, 0, len(s.Days))
		for _, d := range s.Days {
			if d >= 0 && d <= 6 {
				names = append(names, weekdayNames[d])
			}
		}
		return fmt.Sprintf("every week on %s at %s", strings.Join(names, ", "), s.Time)
	case KindInterval:
		switch {
		case s.Every%(24*60) == 0:
			return plural(s.Every/(24*60), "day")
		case s.Every%60 == 0:
			return plural(s.Every/60, "hour")
		default:
			return plural(s.Every, "minute")
		}
	}
	return "on an unknown schedule"
}

func plural(n int, unit string) string {
	if n == 1 {
		return "every " + unit
	}
	return fmt.Sprintf("every %d %ss", n, unit)
}

// window is how late an occurrence of this spec may run.
func (s Spec) window() time.Duration {
	if s.Kind == KindInterval && s.Every > 0 {
		return min(OnTime, time.Duration(s.Every)*time.Minute/2)
	}
	return OnTime
}
