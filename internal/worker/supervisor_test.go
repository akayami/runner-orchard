package worker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
)

type testListener struct {
	running, busy bool
	starts, stops int
}

func (l *testListener) Running() (bool, error) { return l.running, nil }
func (l *testListener) Busy() (bool, error)    { return l.busy, nil }
func (l *testListener) Start() error {
	l.running = true
	l.starts++
	return nil
}
func (l *testListener) Stop() error {
	l.running = false
	l.stops++
	return nil
}

type testRouting struct {
	eligible, busy, unavailable bool
}

func (r *testRouting) SetEligible(_ context.Context, eligible bool) (fleet.WindowsEligibility, error) {
	if r.unavailable {
		return fleet.WindowsEligibility{}, errors.New("control unavailable")
	}
	r.eligible = eligible
	return fleet.WindowsEligibility{Eligible: eligible, Busy: r.busy, Online: true}, nil
}

func TestSupervisorDrainsThenResumesOnAC(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	ac := true
	listener := &testListener{}
	routing := &testRouting{}
	s := Supervisor{Pause: PauseFile(filepath.Join(t.TempDir(), "pause")), Power: func() *bool { return &ac }, Listener: listener, Remote: routing}
	step := func(at time.Time) {
		t.Helper()
		if _, err := s.Step(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	step(now)
	if listener.starts != 1 || routing.eligible {
		t.Fatal("listener must start with the label removed")
	}
	step(now)
	if !routing.eligible {
		t.Fatal("AC worker did not become routable")
	}
	ac, routing.busy, listener.busy = false, true, true
	step(now)
	if routing.eligible || listener.stops != 0 {
		t.Fatal("busy drain must remove the label and keep the listener")
	}
	listener.busy = false
	step(now.Add(time.Minute))
	if listener.stops != 0 {
		t.Fatal("GitHub still reports an assigned job")
	}
	routing.busy = false
	step(now.Add(time.Minute))
	step(now.Add(time.Minute + 14*time.Second))
	if listener.stops != 0 {
		t.Fatal("stopped before the idle observation window")
	}
	step(now.Add(time.Minute + 15*time.Second))
	if listener.stops != 1 {
		t.Fatal("idle listener remained running")
	}
	ac = true
	step(now.Add(2 * time.Minute))
	if listener.starts != 2 {
		t.Fatal("return to AC did not restart listener")
	}
}

func TestSupervisorFailsClosedWhenControlIsUnavailable(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	ac := true
	listener := &testListener{running: true, busy: true}
	s := Supervisor{Pause: PauseFile(filepath.Join(t.TempDir(), "pause")), Power: func() *bool { return &ac },
		Listener: listener, Remote: &testRouting{unavailable: true}}
	for _, offset := range []time.Duration{0, time.Minute} {
		if status, err := s.Step(ctx, now.Add(offset)); err == nil || status.Eligible || listener.stops != 0 {
			t.Fatalf("busy worker was not preserved on outage: %+v %v", status, err)
		}
	}
	listener.busy = false
	_, _ = s.Step(ctx, now.Add(2*time.Minute))
	_, _ = s.Step(ctx, now.Add(3*time.Minute))
	_, _ = s.Step(ctx, now.Add(4*time.Minute))
	if listener.stops != 1 || listener.starts != 0 {
		t.Fatal("idle worker was not kept stopped during outage")
	}
}
