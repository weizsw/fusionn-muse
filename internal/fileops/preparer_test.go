package fileops

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

type fakeRunner struct {
	calls []commandCall
}

type commandCall struct {
	name string
	args []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, commandCall{name: name, args: args})
	return nil
}

func TestConcatVideosUsesFFmpegCopyFirst(t *testing.T) {
	root := t.TempDir()
	parts := []string{
		filepath.Join(root, "ABC-001-part1.mkv"),
		filepath.Join(root, "ABC-001-part2.mkv"),
	}
	out := filepath.Join(root, "prepared", "ABC-001.mkv")
	runner := &fakeRunner{}

	got, err := concatVideos(context.Background(), runner, parts, out)
	if err != nil {
		t.Fatalf("concatVideos returned error: %v", err)
	}
	if got != out {
		t.Fatalf("prepared path = %q, want %q", got, out)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}

	call := runner.calls[0]
	if call.name != "ffmpeg" {
		t.Fatalf("command = %q, want ffmpeg", call.name)
	}
	wantPrefix := []string{"-y", "-f", "concat", "-safe", "0", "-i"}
	if len(call.args) != 10 {
		t.Fatalf("ffmpeg args = %#v, want 10 args", call.args)
	}
	if !reflect.DeepEqual(call.args[:6], wantPrefix) {
		t.Fatalf("ffmpeg args prefix = %#v, want %#v", call.args[:6], wantPrefix)
	}
	if call.args[6] == "" {
		t.Fatal("concat list path is empty")
	}
	wantSuffix := []string{"-c", "copy", out}
	if !reflect.DeepEqual(call.args[7:], wantSuffix) {
		t.Fatalf("ffmpeg args suffix = %#v, want %#v", call.args[7:], wantSuffix)
	}
}

func TestExtractISOUsesBsdtar(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "ABC-001.iso")
	outDir := filepath.Join(root, "extracted")
	runner := &fakeRunner{}

	if err := extractImage(context.Background(), runner, image, outDir); err != nil {
		t.Fatalf("extractImage returned error: %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}

	call := runner.calls[0]
	if call.name != "bsdtar" {
		t.Fatalf("command = %q, want bsdtar", call.name)
	}
	wantArgs := []string{"-xf", image, "-C", outDir}
	if !reflect.DeepEqual(call.args, wantArgs) {
		t.Fatalf("bsdtar args = %#v, want %#v", call.args, wantArgs)
	}
}
