//go:build !windows

package main

import (
	"encoding/json"
	"errors"
)

func supervise(*json.Decoder) error {
	return errors.New("worker supervision requires Windows")
}
