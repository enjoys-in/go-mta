package delivery

import (
	"fmt"
	"sync"
)

// AdapterConstructor is a function that creates a new Adapter instance.
type AdapterConstructor func() Adapter

var (
	mu       sync.RWMutex
	registry = make(map[string]AdapterConstructor)
)

// Register makes a delivery adapter available by name.
// Call this from each adapter's init() function.
func Register(name string, ctor AdapterConstructor) {
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[name]; exists {
		panic(fmt.Sprintf("go-mta: delivery adapter %q already registered", name))
	}
	registry[name] = ctor
}

// New creates an adapter by its registered name.
func New(name string) (Adapter, error) {
	mu.RLock()
	ctor, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("go-mta: unknown delivery adapter %q", name)
	}
	return ctor(), nil
}

// Available returns the names of all registered adapters.
func Available() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	return names
}
