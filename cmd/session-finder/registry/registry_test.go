package registry

import "testing"

func TestNamesAndRun(t *testing.T) {
	name := "test-command-registry"
	called := false
	Register(name, func(argv []string) error {
		called = len(argv) == 1 && argv[0] == "ok"
		return nil
	})
	found, err := Run(name, []string{"ok"})
	if err != nil || !found || !called {
		t.Fatalf("Run() found=%v called=%v err=%v", found, called, err)
	}
	names := Names()
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("Names() not sorted: %v", names)
		}
	}
	if _, ok := Lookup("missing-command-registry"); ok {
		t.Fatal("Lookup found an unregistered command")
	}
	if found, err := Run("missing-command-registry", nil); found || err != nil {
		t.Fatalf("missing Run() = found=%v err=%v", found, err)
	}
}
