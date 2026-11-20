package source

import "testing"

// UseFormats replaces the format registry for one test (for external test
// packages).
var UseFormats = useFormats

// useFormats replaces the registry for one test.
func useFormats(t *testing.T, fs ...Format) {
	t.Helper()
	regMu.Lock()
	saved := registry
	registry = map[string]Format{}
	regMu.Unlock()
	t.Cleanup(func() {
		regMu.Lock()
		registry = saved
		regMu.Unlock()
	})
	for _, f := range fs {
		Register(f)
	}
}
