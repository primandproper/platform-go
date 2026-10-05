package settings

import (
	"testing"

	"github.com/primandproper/platform-go/v15/conformance"
	"github.com/primandproper/platform-go/v15/settings/settingspb"
)

// surface is this suite's name, and the key a subject's per-surface scope is
// read by.
const surface = "settings"

// The calls this suite makes, as the names a caller is minted to make them by.
const (
	archiveDefinition       = settingspb.SettingsService_ArchiveDefinition_FullMethodName
	clearValue              = settingspb.SettingsService_ClearValue_FullMethodName
	createDefinition        = settingspb.SettingsService_CreateDefinition_FullMethodName
	getDefinition           = settingspb.SettingsService_GetDefinition_FullMethodName
	getDefinitionByName     = settingspb.SettingsService_GetDefinitionByName_FullMethodName
	getValue                = settingspb.SettingsService_GetValue_FullMethodName
	listDefinitions         = settingspb.SettingsService_ListDefinitions_FullMethodName
	listValuesForDefinition = settingspb.SettingsService_ListValuesForDefinition_FullMethodName
	listValuesForSubject    = settingspb.SettingsService_ListValuesForSubject_FullMethodName
	resolve                 = settingspb.SettingsService_Resolve_FullMethodName
	resolveAll              = settingspb.SettingsService_ResolveAll_FullMethodName
	setValue                = settingspb.SettingsService_SetValue_FullMethodName
	updateDefinition        = settingspb.SettingsService_UpdateDefinition_FullMethodName
)

// Suite is the settings surface's behavioral assertions.
func Suite() conformance.Suite {
	return conformance.Suite{
		Name:    surface,
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
