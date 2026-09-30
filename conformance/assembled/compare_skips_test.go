package assembled_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/conformance"

	"github.com/shoenig/test"
)

func TestCompareSkips(t *testing.T) {
	t.Parallel()

	expected := []expectedSkip{
		{test: "pagination/settings_ListValuesForDefinition/*"},
		{test: "reservations"},
		{test: "gone"},
	}

	made := []conformance.Skipped{
		{Test: "pagination/settings_ListValuesForDefinition/a_cursor_is_echoed_as_the_previous_one"},
		{Test: "pagination/settings_ListValuesForDefinition/deeper/still"},
		{Test: "reservations"},
		{Test: "signin/refresh/an_exchange_mints_a_successor_in_the_same_login", Reason: "no refresh token"},
	}

	unexpected, unfired := compareSkips(made, expected)

	test.Eq(t, []conformance.Skipped{
		// A pattern names one level, so a deeper test is its own entry.
		{Test: "pagination/settings_ListValuesForDefinition/deeper/still"},
		{Test: "signin/refresh/an_exchange_mints_a_successor_in_the_same_login", Reason: "no refresh token"},
	}, unexpected)
	test.Eq(t, []expectedSkip{{test: "gone"}}, unfired)
}
