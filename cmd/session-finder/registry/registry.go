// Package registry provides the self-registering command boundary used by the
// session-finder CLI. Feature packages can import this package and call
// Register from init without changing the central dispatcher.
package registry

import (
	"sort"
	"strings"
)

// Handler executes a subcommand's argv and returns a user-facing error.
type Handler func([]string) error

var commands = map[string]Handler{}

// Register adds one subcommand. Duplicate names and invalid handlers panic at
// initialization time rather than silently shadowing an existing command.
func Register(name string, run Handler) {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("session-finder: command name must not be empty")
	}
	if run == nil {
		panic("session-finder: command handler must not be nil")
	}
	if _, exists := commands[name]; exists {
		panic("session-finder: duplicate command " + name)
	}
	commands[name] = run
}

// Names returns registered command names in stable display order.
func Names() []string {
	result := make([]string, 0, len(commands))
	for name := range commands {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// Lookup returns a registered command handler.
func Lookup(name string) (Handler, bool) {
	handler, ok := commands[name]
	return handler, ok
}

// Run invokes a registered command, reporting whether it was found.
func Run(name string, argv []string) (bool, error) {
	handler, ok := Lookup(name)
	if !ok {
		return false, nil
	}
	return true, handler(argv)
}
