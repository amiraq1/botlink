package builtin

import (
	"github.com/amiraq1/botlink/internal/tools"
)

// RegisterAll registers all builtin tools into the specified registry.
func RegisterAll(r *tools.Registry) error {
	builtins := []tools.Tool{
		NewGetTimeTool(),
		NewCalculateTool(),
	}

	for _, t := range builtins {
		if err := r.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// RegisterDefaultAll registers all builtin tools into the global DefaultRegistry.
func RegisterDefaultAll() error {
	return RegisterAll(tools.DefaultRegistry)
}

func init() {
	_ = RegisterDefaultAll()
}
