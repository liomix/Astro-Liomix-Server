package id_test

import (
	"regexp"
	"testing"

	"github.com/liomix/astro-liomix-server/internal/platform/id"
)

func TestNewIsURLSafeAndUnique(t *testing.T) {
	t.Parallel()

	pattern := regexp.MustCompile(`^[0-9a-f]{32}$`)
	seen := make(map[string]struct{}, 1000)

	for i := 0; i < 1000; i++ {
		generated := id.New()
		if !pattern.MatchString(generated) {
			t.Fatalf("New() = %q, want 32 lowercase hex characters", generated)
		}
		if _, duplicate := seen[generated]; duplicate {
			t.Fatalf("New() returned duplicate %q", generated)
		}
		seen[generated] = struct{}{}
	}
}

func TestRandomGenerator(t *testing.T) {
	t.Parallel()

	var generator id.Generator = id.RandomGenerator{}
	if generator.New() == "" {
		t.Fatal("RandomGenerator.New() = \"\", want an identifier")
	}
}
