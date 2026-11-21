package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/akayami/runner-orchard/internal/worker"
)

func supervise(dec *json.Decoder) error {
	var config worker.ServiceConfig
	if err := dec.Decode(&config); err != nil {
		return fmt.Errorf("invalid Windows supervisor configuration: %w", err)
	}
	if !filepath.IsAbs(config.RunnerDir) || !filepath.IsAbs(config.PauseFile) || !filepath.IsAbs(config.TokenFile) {
		return fmt.Errorf("Windows supervisor paths must be absolute")
	}
	return worker.RunService(config)
}
