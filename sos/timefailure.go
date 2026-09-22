package sos

import "time"

// Reserved time failures from the time specification. Every constructor
// returns a typed failure so handlers can match the reserved kinds while the
// runtime keeps its ordinary fatal-outcome behavior for run cancellation.

func timeFailureValue(kind, message string, fields map[string]any) *typedFailure {
	value := map[string]any{
		"kind": kind, "message": message, "retryable": false,
	}
	for name, field := range fields {
		value[name] = field
	}
	return &typedFailure{kind: kind, value: value}
}

func invalidDuration(value, reason string) error {
	return timeFailureValue("InvalidDuration", "invalid duration "+value+": "+reason, map[string]any{
		"value": value, "reason": reason,
	})
}

func invalidTimestamp(value, reason string) error {
	return timeFailureValue("InvalidTimestamp", "invalid timestamp "+value+": "+reason, map[string]any{
		"value": value, "reason": reason,
	})
}

func invalidTimeZone(zone string) error {
	return timeFailureValue("InvalidTimeZone", "unknown time zone "+zone, map[string]any{
		"zone": zone,
	})
}

func invalidTimeFormat(value, format string) error {
	return timeFailureValue("InvalidTimeFormat", "value does not match format "+format, map[string]any{
		"value": value, "format": format,
	})
}

func invalidSchedule(reason string) error {
	return timeFailureValue("InvalidSchedule", "invalid schedule: "+reason, map[string]any{
		"reason": reason,
	})
}

// deadlineExceededFailure reports a scoped-deadline breach with the allowed
// budget and the elapsed time measured on the owning clock.
func deadlineExceededFailure(allowed, elapsed time.Duration) error {
	return timeFailureValue("DeadlineExceeded", "deadline exceeded", map[string]any{
		"allowed": allowed, "elapsed": elapsed,
	})
}

func timerLimitExceeded(limit int, operation string) error {
	return timeFailureValue("TimerLimitExceeded", "timer limit exceeded during "+operation, map[string]any{
		"limit": int64(limit), "operation": operation,
	})
}

func scheduleCheckpointFailed(identity, reason string) error {
	return timeFailureValue("ScheduleCheckpointFailed", "schedule checkpoint "+identity+" failed: "+reason, map[string]any{
		"identity": identity, "reason": reason,
	})
}
