package xtractr

import (
	"os"
	"path/filepath"
	"testing"
)

const (
	payload = "the complete extracted payload"
	// short is what an interrupted extraction leaves at the destination.
	short = "the complete"
)

func newTestQueue(t *testing.T, replace func(*ReplaceCheck) bool) *Xtractr {
	t.Helper()

	xtractr := NewQueue(&Config{
		Parallel:        1,
		Suffix:          "_unpackerred",
		FileMode:        0o644,
		DirMode:         0o755,
		ReplaceExisting: replace,
	})
	t.Cleanup(xtractr.Stop)

	return xtractr
}

// dirs returns a temporary and a destination folder, both created.
func dirs(t *testing.T) (string, string) {
	t.Helper()

	base := t.TempDir()
	fromDir := filepath.Join(base, "temp")
	toDir := filepath.Join(base, "final")

	for _, dir := range []string{fromDir, toDir} {
		err := os.MkdirAll(dir, 0o750)
		if err != nil {
			t.Fatal(err)
		}
	}

	return fromDir, toDir
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()

	err := os.WriteFile(path, []byte(data), 0o600)
	if err != nil {
		t.Fatal(err)
	}
}

// A short file left behind by an interrupted extraction must not shield itself
// from repair. Because it occupies the destination name, MoveFiles refused to
// touch it, so every later extraction of the same archive failed identically.
func TestMoveFilesReplaceTruncated(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		existing string
		replace  func(*ReplaceCheck) bool
		want     string
	}{
		{"truncated prefix is replaced", short, ReplaceTruncated, payload},
		{"empty file is replaced", "", ReplaceTruncated, payload},
		{"identical file is kept", payload, ReplaceTruncated, payload},
		{"longer file is kept", payload + " and more", ReplaceTruncated, payload + " and more"},
		{"shorter but not a prefix is kept", "XXX complete", ReplaceTruncated, "XXX complete"},
		{"shorter, differing only at its last byte, is kept", "the completX", ReplaceTruncated, "the completX"},
		{"nil policy keeps the historical behavior", short, nil, short},
		{"policy may accept anything", payload + " and more", func(*ReplaceCheck) bool { return true }, payload},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fromDir, toDir := dirs(t)
			writeFile(t, filepath.Join(fromDir, "payload.bin"), payload)
			writeFile(t, filepath.Join(toDir, "payload.bin"), test.existing)

			_, err := newTestQueue(t, test.replace).MoveFiles(fromDir, toDir, false)
			if err != nil {
				t.Fatalf("MoveFiles: %v", err)
			}

			got, err := os.ReadFile(filepath.Join(toDir, "payload.bin"))
			if err != nil {
				t.Fatal(err)
			}

			if string(got) != test.want {
				t.Errorf("destination = %q, want %q", got, test.want)
			}
		})
	}
}

// A symlink at the destination must never be replaced, and must never be
// written through, whatever the policy returns. Archives can carry symlinks, so
// following one would let an archive redirect a write outside the destination.
func TestMoveFilesKeepsSymlinkDestination(t *testing.T) {
	t.Parallel()

	fromDir, toDir := dirs(t)
	outside := filepath.Join(t.TempDir(), "outside.bin")
	writeFile(t, outside, "do not touch")
	writeFile(t, filepath.Join(fromDir, "payload.bin"), payload)

	err := os.Symlink(outside, filepath.Join(toDir, "payload.bin"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = newTestQueue(t, func(*ReplaceCheck) bool { return true }).MoveFiles(fromDir, toDir, false)
	if err != nil {
		t.Fatalf("MoveFiles: %v", err)
	}

	target, err := os.Readlink(filepath.Join(toDir, "payload.bin"))
	if err != nil {
		t.Fatalf("destination is no longer a symlink: %v", err)
	}

	if target != outside {
		t.Errorf("symlink target = %q, want %q", target, outside)
	}

	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "do not touch" {
		t.Errorf("wrote through the symlink: %q", got)
	}
}

// GetFileList returns directory entries alongside regular files, so a folder
// can occupy a destination name. It must never be considered for replacement.
func TestMoveFilesKeepsDirectoryDestination(t *testing.T) {
	t.Parallel()

	fromDir, toDir := dirs(t)
	writeFile(t, filepath.Join(fromDir, "payload.bin"), payload)

	nested := filepath.Join(toDir, "payload.bin")

	err := os.MkdirAll(nested, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(nested, "keep.txt"), "still here")

	_, err = newTestQueue(t, func(*ReplaceCheck) bool { return true }).MoveFiles(fromDir, toDir, false)
	if err != nil {
		t.Fatalf("MoveFiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(nested, "keep.txt"))
	if err != nil {
		t.Fatalf("directory destination was clobbered: %v", err)
	}

	if string(got) != "still here" {
		t.Errorf("directory contents = %q", got)
	}
}

// A folder extracted from an archive is moved as a unit. When the destination
// is free the folder lands intact, with the files below it untouched.
func TestMoveFilesMovesNestedDirectory(t *testing.T) {
	t.Parallel()

	fromDir, toDir := dirs(t)
	nested := filepath.Join(fromDir, "subdir")

	err := os.MkdirAll(nested, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(nested, "payload.bin"), payload)

	_, err = newTestQueue(t, ReplaceTruncated).MoveFiles(fromDir, toDir, false)
	if err != nil {
		t.Fatalf("MoveFiles: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(toDir, "subdir", "payload.bin"))
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != payload {
		t.Errorf("nested file = %q, want %q", got, payload)
	}
}

// A truncated file larger than one read chunk must still be recognised, and a
// difference past the first chunk must still be caught.
func TestReplaceTruncatedAcrossChunks(t *testing.T) {
	t.Parallel()

	const size = prefixChunk*3 + 17

	full := make([]byte, size)
	for i := range full {
		full[i] = byte(i % 251)
	}

	for _, test := range []struct {
		name   string
		mangle func([]byte) []byte
		want   bool
	}{
		{"exact prefix", func(b []byte) []byte { return b }, true},
		{"differs in the final chunk", func(b []byte) []byte {
			b[len(b)-1] ^= 0xFF

			return b
		}, false},
		{"differs on the first byte", func(b []byte) []byte {
			b[0] ^= 0xFF

			return b
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fromDir, toDir := dirs(t)
			src := filepath.Join(fromDir, "payload.bin")
			dest := filepath.Join(toDir, "payload.bin")

			err := os.WriteFile(src, full, 0o600)
			if err != nil {
				t.Fatal(err)
			}

			partial := test.mangle(append([]byte(nil), full[:prefixChunk*2+5]...))

			err = os.WriteFile(dest, partial, 0o600)
			if err != nil {
				t.Fatal(err)
			}

			_, err = newTestQueue(t, ReplaceTruncated).MoveFiles(fromDir, toDir, false)
			if err != nil {
				t.Fatalf("MoveFiles: %v", err)
			}

			got, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}

			if replaced := len(got) == size; replaced != test.want {
				t.Errorf("replaced = %v, want %v (destination is %d bytes)", replaced, test.want, len(got))
			}
		})
	}
}
