package buildinfo

import "testing"

func TestStringIncludesProgramAndBuildMetadata(t *testing.T) {
	t.Parallel()

	got := String("amc")
	want := "amc dev (commit unknown, built unknown)"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
