package worker

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

const DefaultPause = 4 * time.Hour

// PauseFile belongs in the service's private state directory. Callers serialize
// changes through the service; local clients never write it directly.
type PauseFile string

func (p PauseFile) Until(now time.Time) (time.Time, error) {
	f, err := os.Open(string(p))
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	value, err := io.ReadAll(io.LimitReader(f, 65))
	if err != nil {
		return time.Time{}, err
	}
	if len(value) > 64 {
		return time.Time{}, errors.New("pause state exceeds size limit")
	}
	expires, err := time.Parse(time.RFC3339Nano, string(bytes.TrimSpace(value)))
	if err != nil || expires.IsZero() {
		return time.Time{}, errors.New("invalid pause expiration")
	}
	if !expires.After(now) {
		return time.Time{}, nil
	}
	return expires, nil
}

// Pause replaces the previous deadline. A nil duration uses the default.
func (p PauseFile) Pause(now time.Time, duration *time.Duration) (time.Time, error) {
	length := DefaultPause
	if duration != nil {
		length = *duration
	}
	if length <= 0 {
		return time.Time{}, errors.New("pause duration must be positive")
	}
	expires := now.Add(length).UTC()
	if err := writeState(string(p), []byte(expires.Format(time.RFC3339Nano)+"\n")); err != nil {
		return time.Time{}, err
	}
	return expires, nil
}

func writeState(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".orchard-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (p PauseFile) Resume() error {
	err := os.Remove(string(p))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
