package git

import (
	"slices"
	"testing"
)

func TestParseLocalRefsAcceptsCanonicalSnapshots(t *testing.T) {
	parsed, err := parseLocalRefs([]byte("+skill\x000\x001\nmain\x001\x001\n"))
	if err != nil {
		t.Fatalf("parseLocalRefs returned an error: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed %d records, want 2", len(parsed))
	}
	if parsed[0].name != "+skill" || parsed[0].current || !parsed[0].occupied {
		t.Errorf("first record = %#v, want canonical linked +skill", parsed[0])
	}
	if parsed[1].name != "main" || !parsed[1].current || !parsed[1].occupied {
		t.Errorf("second record = %#v, want current main", parsed[1])
	}

	empty, err := parseLocalRefs(nil)
	if err != nil || !slices.Equal(empty, []localRefInfo{}) {
		t.Errorf("empty snapshot = %#v, %v; want valid empty snapshot", empty, err)
	}
}

func TestParseLocalRefsRejectsMalformedSnapshots(t *testing.T) {
	for name, output := range map[string]string{
		"empty name":       "\x000\x000\n",
		"missing field":    "main\x001\n",
		"extra field":      "main\x001\x000\x00extra\n",
		"unknown current":  "main\x00*\x000\n",
		"unknown occupied": "main\x000\x00yes\n",
		"partial record":   "main\x001\x000",
		"extra record":     "main\x001\x000\n\n",
		"multiple current": "main\x001\x001\nother\x001\x000\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLocalRefs([]byte(output)); err == nil {
				t.Errorf("parseLocalRefs accepted malformed output %q", output)
			}
		})
	}
}
