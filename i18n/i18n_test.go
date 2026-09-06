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
