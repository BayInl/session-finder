package main

import (
	commandregistry "github.com/BayInl/session-finder/cmd/session-finder/registry"
	"github.com/BayInl/session-finder/internal/decisions"
	_ "github.com/BayInl/session-finder/internal/skill"
)

func init() {
	commandregistry.Register("hooks", runHooks)
	commandregistry.Register("index", runIndex)
	commandregistry.Register("search", runSearch)
	commandregistry.Register("show", runShow)
	commandregistry.Register("tui", runTUI)
	commandregistry.Register("version", runVersion)
	decisions.RegisterCommand()
}

func runRegistered(name string, argv []string) error {
	found, err := commandregistry.Run(name, argv)
	if !found {
		return usageError("unknown command: " + name)
	}
	return err
}
