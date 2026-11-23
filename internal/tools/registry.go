package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrToolNotFound      = errors.New("tool not found")
	ErrToolAlreadyExists = errors.New("tool already registered")
)

// Tool defines the Go interface that every builtin tool must implement.
type Tool interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, in json.RawMessage) (Result, error)
}

// Result is the output of a tool execution.
type Result struct {
	Text string `json:"text"`
}

// Registry stores and manages in-memory builtin tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates a new empty Tool Registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// DefaultRegistry is the process-wide default registry for builtin tools.
var DefaultRegistry = NewRegistry()

// Register registers a builtin tool. Returns ErrToolAlreadyExists if a tool with
// the same name is already registered.
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return errors.New("nil tool")
	}
	name := t.Name()
	if name == "" {
		return errors.New("tool name cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("%w: %s", ErrToolAlreadyExists, name)
	}
	r.tools[name] = t
	return nil
}

// Get retrieves a registered tool by its name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools sorted by name.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// Global convenience wrappers on DefaultRegistry

// Register registers a tool in the DefaultRegistry.
func Register(t Tool) error {
	return DefaultRegistry.Register(t)
}

// Get retrieves a tool from the DefaultRegistry.
func Get(name string) (Tool, bool) {
	return DefaultRegistry.Get(name)
}

// List returns all tools from the DefaultRegistry.
func List() []Tool {
	return DefaultRegistry.List()
}
