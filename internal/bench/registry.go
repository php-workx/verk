package bench

import (
	"fmt"
	"sort"
)

// Registry holds in-process provider instances.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register adds a provider to the registry. Returns an error if a provider
// with the same name is already registered.
func (r *Registry) Register(p Provider) error {
	name := p.Name()
	if _, exists := r.providers[name]; exists {
		return fmt.Errorf("bench: provider %q is already registered", name)
	}
	r.providers[name] = p
	return nil
}

// Get retrieves a provider by name. Returns false if not found.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// List returns the names of all registered providers in sorted order.
func (r *Registry) List() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
