package mediaintake

import "testing"

func TestCleanVideoFilenameUsesMediaIntakePackage(t *testing.T) {
	got := CleanVideoFilename("xxxSSNI-083-C.mp4")
	if got != "SSNI-083.mp4" {
		t.Fatalf("CleanVideoFilename = %q, want SSNI-083.mp4", got)
	}
}
