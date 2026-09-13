package i18n

import (
	"reflect"
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
