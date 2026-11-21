package worker

import "time"

type Status struct {
	State      string     `json:"state"`
	PauseUntil *time.Time `json:"pause_until,omitempty"`
	Busy       bool       `json:"busy"`
	Eligible   bool       `json:"eligible"`
}

// Eligibility is the desired routing state. The service must separately confirm
// GitHub label changes and listener state before reporting the worker online.
func Eligibility(now, pauseUntil time.Time, ac *bool, healthy, busy bool) Status {
	status := Status{Busy: busy}
	if pauseUntil.After(now) {
		status.PauseUntil = &pauseUntil
	}
	switch {
	case !healthy:
		status.State = "offline-error"
	case status.PauseUntil != nil && busy:
		status.State = "draining-paused"
	case status.PauseUntil != nil:
		status.State = "paused"
	case ac == nil:
		status.State = "unknown-power"
	case !*ac:
		status.State = "draining-battery"
	default:
		status.State = "eligible"
		status.Eligible = true
	}
	return status
}
