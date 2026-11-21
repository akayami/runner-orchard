package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/akayami/runner-orchard/internal/collector"
	"github.com/akayami/runner-orchard/internal/worker"
)

func workerCommand(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	request := worker.LocalRequest{Command: command}
	var hours *float64
	if command == "pause" {
		hours = flags.Float64("hours", worker.DefaultPause.Hours(), "hours to pause")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected command arguments")
	}
	request.Hours = hours
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	status, err := worker.LocalCommand(ctx, request)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}

func workerPowerCommand(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("file", "", "protected power plan snapshot")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*path) {
		return fmt.Errorf("-file must be an absolute snapshot path")
	}
	if command == "power-snapshot" {
		return worker.SnapshotPowerPlan(*path)
	}
	return worker.ApplyPowerPlan(*path, false)
}

func supervise(dec *json.Decoder) error {
	var config struct {
		worker.ServiceConfig
		Collector collector.Config `json:"collector"`
	}
	if err := dec.Decode(&config); err != nil {
		return fmt.Errorf("invalid Windows supervisor configuration: %w", err)
	}
	if !filepath.IsAbs(config.RunnerDir) || !filepath.IsAbs(config.PauseFile) || !filepath.IsAbs(config.TokenFile) {
		return fmt.Errorf("Windows supervisor paths must be absolute")
	}
	if config.PowerPlanFile != "" && !filepath.IsAbs(config.PowerPlanFile) {
		return fmt.Errorf("power plan snapshot path must be absolute")
	}
	return worker.RunService(config.ServiceConfig, func(ctx context.Context, status func() worker.Status) {
		if config.Collector.Endpoint != "" {
			config.Collector.WorkerStatus = status
			if err := collector.Run(ctx, config.Collector); err != nil {
				log.Print("Windows collector exited: ", err)
			}
		}
	})
}
