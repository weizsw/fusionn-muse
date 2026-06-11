package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeRunner struct {
	calls  []commandCall
	errors []error
}

type commandCall struct {
	name string
	args []string
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	r.calls = append(r.calls, commandCall{name: name, args: args})
	if len(r.errors) > 0 {
		err := r.errors[0]
		r.errors = r.errors[1:]
		return err
	}
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

func TestConcatVideosFallsBackToTranscode(t *testing.T) {
	root := t.TempDir()
	parts := []string{
		filepath.Join(root, "ABC-001-part1.mkv"),
		filepath.Join(root, "ABC-001-part2.mkv"),
	}
	out := filepath.Join(root, "prepared", "ABC-001.mkv")
	runner := &fakeRunner{errors: []error{errors.New("copy failed")}}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		t.Fatalf("mkdir output dir: %v", err)
	}
	if err := os.WriteFile(out, []byte("partial"), 0644); err != nil {
		t.Fatalf("write partial output: %v", err)
	}

	got, err := concatVideos(context.Background(), runner, parts, out)
	if err != nil {
		t.Fatalf("concatVideos returned error: %v", err)
	}

	want := filepath.Join(root, "prepared", "ABC-001.mp4")
	if got != want {
		t.Fatalf("prepared path = %q, want %q", got, want)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(runner.calls))
	}
	call := runner.calls[1]
	if call.name != "ffmpeg" {
		t.Fatalf("fallback command = %q, want ffmpeg", call.name)
	}
	wantSuffix := []string{"-c:v", "libx264", "-c:a", "aac", want}
	if !reflect.DeepEqual(call.args[len(call.args)-5:], wantSuffix) {
		t.Fatalf("fallback args suffix = %#v, want %#v", call.args[len(call.args)-5:], wantSuffix)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial copy output still exists, stat error = %v", err)
	}
}

func TestWriteConcatListEscapesPaths(t *testing.T) {
	root := t.TempDir()
	listPath := filepath.Join(root, "concat.txt")
	parts := []string{
		filepath.Join(root, "part one.mkv"),
		filepath.Join(root, "director's cut.mkv"),
	}

	if err := writeConcatList(listPath, parts); err != nil {
		t.Fatalf("writeConcatList returned error: %v", err)
	}
	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatalf("read concat list: %v", err)
	}

	got := string(data)
	if !strings.Contains(got, "part one.mkv") {
		t.Fatalf("concat list = %q, want path with space", got)
	}
	if !strings.Contains(got, "director'\\''s cut.mkv") {
		t.Fatalf("concat list = %q, want escaped apostrophe", got)
	}
}

func TestExtractNRGConvertsISOInOutputDir(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(root, "source", "ABC-001.nrg")
	outDir := filepath.Join(root, "extracted")
	runner := &fakeRunner{errors: []error{errors.New("7z failed")}}

	if err := extractImage(context.Background(), runner, image, outDir); err != nil {
		t.Fatalf("extractImage returned error: %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("runner calls = %d, want 3", len(runner.calls))
	}

	isoPath := filepath.Join(outDir, "ABC-001.iso")
	if runner.calls[1].name != "nrg2iso" {
		t.Fatalf("fallback command = %q, want nrg2iso", runner.calls[1].name)
	}
	wantConvertArgs := []string{image, isoPath}
	if !reflect.DeepEqual(runner.calls[1].args, wantConvertArgs) {
		t.Fatalf("nrg2iso args = %#v, want %#v", runner.calls[1].args, wantConvertArgs)
	}
	wantExtractArgs := []string{"-xf", isoPath, "-C", outDir}
	if !reflect.DeepEqual(runner.calls[2].args, wantExtractArgs) {
		t.Fatalf("bsdtar args = %#v, want %#v", runner.calls[2].args, wantExtractArgs)
	}
}
