package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestKindOfAndIs(t *testing.T) {
	sentinel := New(KindConfig, "invalid llm provider")
	wrapped := fmt.Errorf("%w: %q", sentinel, "anthropic")
	if !errors.Is(wrapped, sentinel) {
		t.Fatal("wrapped sentinel must match errors.Is")
	}
	if KindOf(wrapped) != KindConfig {
		t.Fatalf("KindOf = %q", KindOf(wrapped))
	}
	if KindOf(errors.New("plain")) != "" {
		t.Fatal("plain error must have empty kind")
	}
	if Wrap(KindNetwork, nil, "x") != nil {
		t.Fatal("Wrap(nil) must be nil")
	}
}
