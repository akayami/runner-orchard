//go:build !windows

package collector

import "github.com/akayami/runner-orchard/internal/fleet"

func windowsPower() *fleet.Power { return nil }
