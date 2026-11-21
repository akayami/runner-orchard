package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsPipeTimedPauseAndResume(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\RunnerOrchardTest-%d-%d`, os.Getpid(), time.Now().UnixNano())
	pause := PauseFile(filepath.Join(t.TempDir(), "pause"))
	control, pipe, err := listenLocalControls(path, pause)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	go control.serve(pipe)
	command := func(req LocalRequest) Status {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		status, err := localCommandAt(ctx, path, req)
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	state := command(LocalRequest{Command: "pause"})
	if state.PauseUntil == nil || time.Until(*state.PauseUntil).Round(time.Minute) != 4*time.Hour || state.Eligible {
		t.Fatalf("unexpected default pause: %+v", state)
	}
	hours := 1.5
	state = command(LocalRequest{Command: "pause", Hours: &hours})
	if state.PauseUntil == nil || time.Until(*state.PauseUntil).Round(time.Minute) != 90*time.Minute {
		t.Fatalf("unexpected replacement pause: %+v", state)
	}
	if state = command(LocalRequest{Command: "status"}); state.PauseUntil == nil {
		t.Fatal("status lost the pause")
	}
	if state = command(LocalRequest{Command: "resume"}); state.PauseUntil != nil || state.Eligible {
		t.Fatalf("resume must trigger fresh eligibility: %+v", state)
	}
	if _, err := os.Stat(string(pause)); !os.IsNotExist(err) {
		t.Fatalf("pause state remains after resume: %v", err)
	}
}
