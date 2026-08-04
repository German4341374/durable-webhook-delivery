package idgen

import (
	"regexp"
	"testing"
)

func TestUUIDFormatAndUniqueness(t *testing.T) {
	first, err := UUID()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := UUID()
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(first) || first == second {
		t.Fatalf("invalid UUIDs: %q %q", first, second)
	}
}
