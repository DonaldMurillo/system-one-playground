package sos

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type timerStreamSource struct {
	one       *OneShotTimer
	repeating *RepeatingTimer
}

func (s *timerStreamSource) next(ctx context.Context) (any, bool, error) {
	if s.one != nil {
		return s.one.Next(ctx)
	}
	return s.repeating.Next(ctx)
}

func (s *timerStreamSource) cancel(context.Context) error {
	if s.one != nil {
		s.one.Stop()
	} else {
		s.repeating.Stop()
	}
	return nil
}

func (s *timerStreamSource) timerSnapshot() TimerSnapshot {
	if s.one != nil {
		return s.one.Snapshot()
	}
	return s.repeating.Snapshot()
}

type scheduleStreamSource struct {
	clock    Clock
	rule     *ScheduleRule
	queue    []time.Time
	sequence int64
	stopped  bool
	zone     string
	label    string
}

func (s *scheduleStreamSource) next(ctx context.Context) (any, bool, error) {
	if s.stopped {
		return nil, false, nil
	}
	if len(s.queue) == 0 {
		occurrences, err := s.rule.NextAfter(s.clock.Now())
		if err != nil {
			return nil, false, err
		}
		s.queue = occurrences
	}
	next := s.queue[0]
	s.queue = s.queue[1:]
	if err := waitClockUntil(ctx, s.clock, next); err != nil {
		return nil, false, err
	}
	s.sequence++
	return newTimeTick(s.sequence, next, s.clock.Now(), 0), true, nil
}

func (s *scheduleStreamSource) cancel(context.Context) error { s.stopped = true; return nil }

func (s *scheduleStreamSource) scheduleSnapshot() (clock, label, zone string, next *time.Time) {
	var value time.Time
	if len(s.queue) > 0 {
		value = s.queue[0]
	} else if occurrences, err := s.rule.NextAfter(s.clock.Now()); err == nil && len(occurrences) > 0 {
		value = occurrences[0]
	}
	if !value.IsZero() {
		next = &value
	}
	return s.clock.Kind(), s.label, s.zone, next
}

func (r *runtime) registerTimeStream(binding string, line int, producer string, source streamSource) *streamHandle {
	stream := newStreamHandleWithClock(r.ctx, r.clock, TypeRef{Name: "TimeTick"}, producer, source)
	stream.id = fmt.Sprintf("stream-%d", r.shared.streams.Add(1))
	stream.binding, stream.line, stream.emit = binding, line, r.opts.OnStreamEvent
	if r.opts.Streams != nil {
		r.opts.Streams.register(stream)
	}
	stream.publish("opened")
	r.env[binding] = stream
	return stream
}

func timerPolicyFromStatement(s *Statement) MissedTickPolicy {
	policy := MissedTickPolicy{Mode: MissedCombine}
	for _, child := range s.Body {
		if child.Kind != "timerPolicy" {
			continue
		}
		m := match("timerPolicy", child.Text)
		switch {
		case strings.HasPrefix(m[1], "skipping"):
			policy.Mode = MissedSkip
		case strings.HasPrefix(m[1], "catching"):
			policy.Mode = MissedCatchUp
			policy.Max, _ = strconv.ParseInt(m[2], 10, 64)
		}
	}
	return policy
}

func scheduleFromStatement(s *Statement, zoneName string) (*ScheduleRule, string, error) {
	zone, err := LoadZone(zoneName)
	if err != nil {
		return nil, "", err
	}
	policy := SchedulePolicy{}
	for _, child := range s.Body {
		if child.Kind != "scheduleNotExist" && child.Kind != "scheduleOccursTwice" {
			continue
		}
		for _, choice := range child.Body {
			switch choice.Text {
			case "use the next valid time":
				policy.Nonexistent = NonexistentNextValid
			case "run only at the second occurrence":
				policy.Ambiguous = AmbiguousSecond
			case "run at both occurrences":
				policy.Ambiguous = AmbiguousBoth
			}
		}
	}
	for _, child := range s.Body {
		switch child.Kind {
		case "scheduleRuleHour":
			rule, e := EveryHourAt(0, zone, policy)
			return rule, "every hour", e
		case "scheduleRuleAt":
			m := match(child.Kind, child.Text)
			hour, _ := strconv.Atoi(m[2])
			minute, _ := strconv.Atoi(m[3])
			if m[1] == "weekday" {
				rule, e := EveryDayAt(hour, minute, zone, policy)
				if e == nil {
					rule.Weekdays[time.Saturday], rule.Weekdays[time.Sunday] = false, false
				}
				return rule, child.Text, e
			}
			weekdays := map[string]time.Weekday{"Sunday": time.Sunday, "Monday": time.Monday, "Tuesday": time.Tuesday, "Wednesday": time.Wednesday, "Thursday": time.Thursday, "Friday": time.Friday, "Saturday": time.Saturday}
			rule, e := EveryWeekdayAt(weekdays[m[1]], hour, minute, zone, policy)
			return rule, child.Text, e
		case "scheduleRuleMonth":
			m := match(child.Kind, child.Text)
			hour, _ := strconv.Atoi(m[2])
			minute, _ := strconv.Atoi(m[3])
			day := 1
			var rule *ScheduleRule
			var e error
			if m[1] == "last" {
				rule, e = MonthlyLastDayAt(hour, minute, zone, policy)
			} else {
				rule, e = MonthlyOn(day, hour, minute, zone, policy)
			}
			return rule, child.Text, e
		}
	}
	return nil, "", invalidSchedule("missing schedule rule")
}
