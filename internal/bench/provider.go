package bench

// Provider is implemented by each benchmark source (e.g. verk-native, aider-polyglot).
type Provider interface {
	Name() string
	Suites() []SuiteMeta
	LoadTasks(suite string) ([]Task, error)
	Capabilities() ProviderCapabilities
}

// ProviderCapabilities describes what a provider supports.
type ProviderCapabilities struct {
	SupportsIsolatedVerifier bool            `json:"supports_isolated_verifier"`
	CachePolicy              string          `json:"cache_policy"` // locked|read-only|read-write
	SupportedModes           []BenchmarkMode `json:"supported_modes,omitempty"`
}

// Task is one benchmark task. Provider-specific data lives in Spec.
type Task struct {
	ID    string         `json:"id"`
	Suite string         `json:"suite"`
	Title string         `json:"title,omitempty"`
	Spec  map[string]any `json:"spec,omitempty"`
}
