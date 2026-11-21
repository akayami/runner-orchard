package worker

import (
	"context"
	"fmt"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
)

// Listener uses the native runner's SCM lifecycle. Busy must inspect only
// this runner's worker processes and return an error if ownership is unknown.
type Listener interface {
	Running() (bool, error)
	Busy() (bool, error)
	Start() error
	Stop() error
}

type Supervisor struct {
	Pause  PauseFile
	Power  func() *bool
	Remote interface {
		SetEligible(context.Context, bool) (fleet.WindowsEligibility, error)
	}
	Listener  Listener
	idleSince time.Time
}

// Step advances the lifecycle once. Run it serially. Removing the routing
// label precedes listener shutdown, with an idle observation window for jobs
// assigned before the label removal propagates.
func (s *Supervisor) Step(ctx context.Context, now time.Time) (Status, error) {
	until, pauseErr := s.Pause.Until(now)
	running, runningErr := s.Listener.Running()
	busy, busyErr := s.Listener.Busy()
	desired := Eligibility(now, until, s.Power(), pauseErr == nil && runningErr == nil && busyErr == nil, busy)
	remote, remoteErr := s.Remote.SetEligible(ctx, desired.Eligible && running)
	if desired.Eligible && remoteErr == nil {
		s.idleSince = time.Time{}
		// A stopped listener first confirms the label is absent. Recheck
		// power immediately before starting it.
		if !running && Eligibility(now, until, s.Power(), true, busy).Eligible {
			if err := s.Listener.Start(); err != nil {
				return failed(desired, err)
			}
		}
		desired.Busy = desired.Busy || remote.Busy
		if running && remote.Online {
			desired.State = "online"
		}
		return desired, nil
	}

	if running && busyErr == nil && !busy && !remote.Busy {
		if s.idleSince.IsZero() {
			s.idleSince = now
		}
		if now.Sub(s.idleSince) >= 15*time.Second {
			if busy, err := s.Listener.Busy(); err != nil {
				return failed(desired, err)
			} else if !busy {
				if err := s.Listener.Stop(); err != nil {
					return failed(desired, err)
				}
				s.idleSince = time.Time{}
			}
		}
	} else {
		s.idleSince = time.Time{}
	}
	for _, err := range []error{pauseErr, runningErr, busyErr, remoteErr} {
		if err != nil {
			return failed(desired, err)
		}
	}
	desired.Busy = desired.Busy || remote.Busy
	return desired, nil
}

func failed(status Status, err error) (Status, error) {
	status.State, status.Eligible = "offline-error", false
	return status, fmt.Errorf("worker supervision: %w", err)
}
