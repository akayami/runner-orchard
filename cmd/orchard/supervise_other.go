//go:build !windows

package main

import (
	"encoding/json"
	"errors"
)

func supervise(*json.Decoder) error {
	return errors.New("worker supervision requires Windows")
}

func workerCommand(string, []string) error {
	return errors.New("local worker control requires Windows")
}

func workerPowerCommand(string, []string) error {
	return errors.New("AC power plan management requires Windows")
}
