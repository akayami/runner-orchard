package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/akayami/runner-orchard/internal/collector"
	"github.com/akayami/runner-orchard/internal/console"
	"github.com/akayami/runner-orchard/internal/fleet"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: orchard serve|collect|supervise -config FILE")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	path := fs.String("config", "", "configuration JSON file")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("-config is required")
	}
	f, err := os.Open(*path)
	if err != nil {
		return fmt.Errorf("cannot open configuration")
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if os.Args[1] == "supervise" {
		return supervise(dec)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if os.Args[1] == "collect" {
		var c collector.Config
		if err := dec.Decode(&c); err != nil {
			return fmt.Errorf("invalid collector configuration: %w", err)
		}
		return collector.Run(ctx, c)
	}
	if os.Args[1] != "serve" {
		return fmt.Errorf("unknown command")
	}
	var c console.Config
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("invalid server configuration: %w", err)
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if ip := os.Getenv("ORCHARD_POD_IP"); net.ParseIP(ip) != nil {
		c.AllowedHosts = append(c.AllowedHosts, ip)
	}
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if c.TokenFile != "" {
		data, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return fmt.Errorf("cannot read GitHub token file")
		}
		token = strings.TrimSpace(string(data))
	}
	state := fleet.New()
	if (c.Provisioning.Enabled || len(c.WindowsWorkers) != 0) && token == "" {
		return fmt.Errorf("provisioning and worker control require a server-side GitHub credential")
	}
	github := fleet.NewGitHub(func() string { return token })
	go func() {
		for {
			github.Poll(ctx, state, c.Scopes, c.Repositories)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(c.PollSeconds) * time.Second):
			}
		}
	}()
	server := &http.Server{Addr: c.Listen, Handler: console.Handler(state, c, github), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Runner Orchard listening on %s; web /, read-only MCP /mcp; history is in memory", c.Listen)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}
