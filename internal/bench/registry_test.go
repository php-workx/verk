package bench

import (
	"fmt"
	"testing"
)

// stubProvider is a minimal Provider implementation for testing.
type stubProvider struct {
	name string
}

func (s *stubProvider) Name() string { return s.name }
func (s *stubProvider) Suites() []SuiteMeta {
	return []SuiteMeta{{Name: "test-suite", Provider: s.name}}
}

func (s *stubProvider) LoadTasks(_ string) ([]Task, error) {
	return []Task{{ID: "t1", Suite: "test-suite"}}, nil
}

func (s *stubProvider) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		CachePolicy: "locked",
		SupportedModes: []BenchmarkMode{
			ModeWorkerOnly,
		},
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{name: "verk-native"}

	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := r.Get("verk-native")
	if !ok {
		t.Fatal("Get: provider not found after registration")
	}
	if got.Name() != "verk-native" {
		t.Errorf("Get: name mismatch: got %q want verk-native", got.Name())
	}
}

func TestRegistry_RejectsDuplicateProvider(t *testing.T) {
	r := NewRegistry()
	p := &stubProvider{name: "verk-native"}

	if err := r.Register(p); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(p); err == nil {
		t.Fatal("expected error on duplicate registration, got nil")
	}
}

func TestRegistry_ListSorted(t *testing.T) {
	r := NewRegistry()
	names := []string{"zebra", "alpha", "mango", "beta"}
	for _, name := range names {
		if err := r.Register(&stubProvider{name: name}); err != nil {
			t.Fatalf("Register %q: %v", name, err)
		}
	}

	list := r.List()
	if len(list) != len(names) {
		t.Fatalf("List: got %d entries want %d", len(list), len(names))
	}

	expected := []string{"alpha", "beta", "mango", "zebra"}
	for i, want := range expected {
		if list[i] != want {
			t.Errorf("List[%d]: got %q want %q", i, list[i], want)
		}
	}
}

func TestRegistry_GetMissing(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Fatal("Get: expected false for missing provider, got true")
	}
}

func TestRegistry_MultipleProviders(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("provider-%d", i)
		if err := r.Register(&stubProvider{name: name}); err != nil {
			t.Fatalf("Register %q: %v", name, err)
		}
	}

	list := r.List()
	if len(list) != 5 {
		t.Errorf("List: got %d entries want 5", len(list))
	}
}
