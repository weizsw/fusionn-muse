package fileops

import "testing"

func TestExtractVideoCode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "hyphenated upper", in: "SSNI-083.mp4", want: "SSNI-083", ok: true},
		{name: "hyphenated lower", in: "sone-269.mp4", want: "SONE-269", ok: true},
		{name: "compact padded", in: "ssni00083hhb.mp4", want: "SSNI-083", ok: true},
		{name: "compact no padding", in: "pppd176A.FHD.wmv", want: "PPPD-176", ok: true},
		{name: "compact padded numeric part", in: "soe00967hhb1.wmv", want: "SOE-967", ok: true},
		{name: "technical token rejected", in: "movie.FHD1080.x265.mp4", want: "", ok: false},
		{name: "hyphenated hd technical prefix rejected", in: "HD-108.mp4", want: "", ok: false},
		{name: "hyphenated fhd technical prefix rejected", in: "FHD-1080.mp4", want: "", ok: false},
		{name: "hyphenated avc technical prefix rejected", in: "AVC-123.mp4", want: "", ok: false},
		{name: "scans past hyphenated technical prefix", in: "FHD-1080.SONE-269.mp4", want: "SONE-269", ok: true},
		{name: "scans past compact technical prefix", in: "movie.FHD1080.SONE269.mp4", want: "SONE-269", ok: true},
		{name: "glued prefix hyphenated", in: "xxxSONE-269.mp4", want: "SONE-269", ok: true},
		{name: "no code", in: "sample-video.mp4", want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractVideoCode(tt.in)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("ExtractVideoCode(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestCleanVideoFilenameUsesCompactCode(t *testing.T) {
	got := CleanVideoFilename("ssni00083hhb.mp4")
	if got != "SSNI-083.mp4" {
		t.Fatalf("CleanVideoFilename compact = %q, want SSNI-083.mp4", got)
	}
}

func TestCleanVideoFilenameUsesGluedHyphenatedCode(t *testing.T) {
	got := CleanVideoFilename("xxxSONE-269.mp4")
	if got != "SONE-269.mp4" {
		t.Fatalf("CleanVideoFilename glued hyphenated = %q, want SONE-269.mp4", got)
	}
}
