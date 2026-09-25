package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"
)

// Suite is the settings surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    "settings",
		Mounted: func(s conformance.Surfaces) bool { return s.Settings != nil },
		Run:     run,
	}
}

func run(t *testing.T, s *conformance.Session) {
	t.Helper()

	t.Run("definitions", func(t *testing.T) {
		t.Parallel()
		definitions(t, s)
	})
	t.Run("values", func(t *testing.T) {
		t.Parallel()
		values(t, s)
	})
	t.Run("confinement", func(t *testing.T) {
		t.Parallel()
		confinement(t, s)
	})
	t.Run("reserved settings", func(t *testing.T) {
		t.Parallel()
		reserved(t, s)
	})
}
