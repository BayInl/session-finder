package main

import (
	"fmt"
	"strings"

	commandregistry "github.com/BayInl/session-finder/cmd/session-finder/registry"
	"github.com/BayInl/session-finder/internal/decisions"
	_ "github.com/BayInl/session-finder/internal/skill"
)

// commandHandler is kept as an alias for existing main-package tests. New
// feature packages should import cmd/session-finder/registry directly.
type commandHandler = commandregistry.Handler

// RegisterCommand forwards registration to the importable command registry.
func RegisterCommand(name string, run commandHandler) { commandregistry.Register(name, run) }

// Commands returns registered command names in stable display order.
func Commands() []string { return commandregistry.Names() }

func init() {
	RegisterCommand("index", runIndex)
	RegisterCommand("search", runSearch)
	RegisterCommand("show", runShow)
	decisions.RegisterCommand()
}

func runRegistered(name string, argv []string) error {
	found, err := commandregistry.Run(name, argv)
	if !found {
		return usageError("unknown command: " + name)
	}
	return err
}

func rootUsage() string {
	return fmt.Sprintf("usage: session-finder <%s> [flags]", strings.Join(Commands(), "|"))
}
