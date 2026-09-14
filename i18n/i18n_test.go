package i18n

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestEveryLocaleTranslatesEveryString(t *testing.T) {
	locales := map[string]*LanguageMapping{
		"en":      &eN,
		"ja":      &jA,
		"zh-hans": &zH_HANS,
		"zh-hant": &zH_HANT,
	}

	// A user-facing string reaches the terminal through LANGUAGEMAPPING, so a
	// field left unset in one locale renders as nothing at all for that user.
	for name, mapping := range locales {
		value := reflect.ValueOf(mapping).Elem()
		for i := 0; i < value.NumField(); i++ {
			if value.Field(i).Kind() != reflect.String {
				continue
			}
			if value.Field(i).String() == "" {
				t.Errorf("%s is missing a translation for %s", name, value.Type().Field(i).Name)
			}
		}
	}
}

func TestPushDiagnosticsFormatLabelsParseInEveryLocale(t *testing.T) {
	locales := map[string]*LanguageMapping{
		"en":      &eN,
		"ja":      &jA,
		"zh-hans": &zH_HANS,
		"zh-hant": &zH_HANT,
	}

	// A broken %s/%d placeholder would render a visible %!x(MISSING) artefact
	// inside the push popup for that locale's users.
	for name, mapping := range locales {
		if out := fmt.Sprintf(mapping.GitPushPopUpCouldNotStart, "cause"); strings.Contains(out, "%!") {
			t.Errorf("%s GitPushPopUpCouldNotStart has a broken placeholder: %q", name, out)
		}
		if out := fmt.Sprintf(mapping.GitPushPopUpNonZeroExit, 1); strings.Contains(out, "%!") {
			t.Errorf("%s GitPushPopUpNonZeroExit has a broken placeholder: %q", name, out)
		}
		if out := fmt.Sprintf(mapping.GitPushPopUpReadFailure, "cause"); strings.Contains(out, "%!") {
			t.Errorf("%s GitPushPopUpReadFailure has a broken placeholder: %q", name, out)
		}
		if out := fmt.Sprintf(mapping.GitPushPopUpRefreshFailed, "branch (read failed)"); strings.Contains(out, "%!") {
			t.Errorf("%s GitPushPopUpRefreshFailed has a broken placeholder: %q", name, out)
		}
	}
}

func TestEveryLocaleTranslatesScreenModeNavigation(t *testing.T) {
	locales := map[string]*LanguageMapping{
		"en":      &eN,
		"ja":      &jA,
		"zh-hans": &zH_HANS,
		"zh-hant": &zH_HANT,
	}

	for name, mapping := range locales {
		if mapping.ScreenModeNavigationKey != "=/_" {
			t.Errorf("%s screen mode key = %q, want %q", name, mapping.ScreenModeNavigationKey, "=/_")
		}
		if mapping.ScreenModeNavigationDescription == "" {
			t.Errorf("%s is missing the screen mode description", name)
		}
	}
}
