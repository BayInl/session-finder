// Package brand holds the user-facing command name.
package brand

// Name is the short CLI people type. LegacyName remains accepted as an alias.
const (
	Name       = "sfind"
	LegacyName = "session-finder"
)

// Usage returns a usage line for the short command name.
func Usage(rest string) string {
	if rest == "" {
		return "usage: " + Name
	}
	return "usage: " + Name + " " + rest
}
