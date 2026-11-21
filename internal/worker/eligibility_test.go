package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTimedPauseLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pause-until")
	pause := PauseFile(path)
	now := time.Date(2026, 11, 21, 16, 0, 0, 0, time.UTC)
	deadline, err := pause.Pause(now, nil)
	if err != nil || !deadline.Equal(now.Add(4*time.Hour)) {
		t.Fatalf("default pause = %v, %v", deadline, err)
	}
	custom := 90 * time.Minute
	deadline, err = pause.Pause(now, &custom)
	if err != nil || !deadline.Equal(now.Add(custom)) {
		t.Fatalf("replacement pause = %v, %v", deadline, err)
	}
	reopened := PauseFile(path)
	if until, err := reopened.Until(now); err != nil || !until.Equal(deadline) {
		t.Fatalf("persisted pause = %v, %v", until, err)
	}
	if until, err := reopened.Until(deadline); err != nil || !until.IsZero() {
		t.Fatalf("expired pause = %v, %v", until, err)
	}
	if err := reopened.Resume(); err != nil {
		t.Fatal(err)
	}
	if until, err := reopened.Until(now); err != nil || !until.IsZero() {
		t.Fatalf("early resume = %v, %v", until, err)
	}
	invalid := time.Duration(0)
	if _, err := pause.Pause(now, &invalid); err == nil {
		t.Fatal("accepted zero duration")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pause.Until(now); err == nil {
		t.Fatal("accepted corrupt pause state")
	}
}

func TestEligibilityTransitions(t *testing.T) {
	now := time.Now()
	deadline := now.Add(time.Hour)
	ac, battery := true, false
	for _, tc := range []struct {
		name          string
		power         *bool
		until         time.Time
		healthy, busy bool
		state         string
		eligible      bool
	}{
		{"on AC", &ac, time.Time{}, true, false, "eligible", true},
		{"unplugged busy", &battery, time.Time{}, true, true, "draining-battery", false},
		{"unknown power", nil, time.Time{}, true, false, "unknown-power", false},
		{"pause requested during job", &ac, deadline, true, true, "draining-paused", false},
		{"paused on AC", &ac, deadline, true, false, "paused", false},
		{"pause survives power cycle", &battery, deadline, true, false, "paused", false},
		{"expired on AC", &ac, now, true, false, "eligible", true},
		{"expired on battery", &battery, now, true, false, "draining-battery", false},
		{"control unavailable", &ac, deadline, false, true, "offline-error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := Eligibility(now, tc.until, tc.power, tc.healthy, tc.busy)
			if status.State != tc.state || status.Eligible != tc.eligible || status.Busy != tc.busy {
				t.Fatalf("unexpected status: %+v", status)
			}
			if (status.PauseUntil != nil) != tc.until.After(now) {
				t.Fatalf("unexpected pause deadline: %+v", status)
			}
		})
	}
}
