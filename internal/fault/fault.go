// Package fault is the shared error kind used by CLI reporting and feature
// packages. Callers wrap sentinels with Kind so PrintError can label TTY
// output without changing pipe text.
package fault

import "errors"

// Kind is a stable, lowercase label for error reports.
type Kind string

const (
	KindInvalid Kind = "invalid"
	KindConfig  Kind = "config"
	KindNetwork Kind = "network"
	KindSchema  Kind = "schema"
	KindOffline Kind = "offline"
	// KindInternal is reserved for violated invariants and implementation
	// failures that do not fit a user-actionable kind.
	KindInternal Kind = "internal"
)

// Error is a kinded wrapper. Error() stays the human message so pipes and
// errors.Is/As keep working; Kind is extra metadata for reports.
type Error struct {
	Kind Kind
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Msg != "" && e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Msg
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok || e == nil || t == nil {
		return false
	}
	if e.Kind != t.Kind {
		return false
	}
	if e.Err == nil && t.Err == nil {
		return e.Msg == t.Msg
	}
	return false
}

// New returns a sentinel-style error with a kind.
func New(kind Kind, msg string) error {
	return &Error{Kind: kind, Msg: msg}
}

// Wrap attaches kind and an optional prefix to err. nil stays nil.
func Wrap(kind Kind, err error, msg string) error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Msg: msg, Err: err}
}

type kinded interface {
	Kind() Kind
}

// KindOf walks the unwrap chain. Unknown errors have empty kind.
func KindOf(err error) Kind {
	var typed *Error
	if errors.As(err, &typed) && typed != nil {
		return typed.Kind
	}
	var k kinded
	if errors.As(err, &k) && k != nil {
		return k.Kind()
	}
	return ""
}
