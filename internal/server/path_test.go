package server

import (
	"path/filepath"
	"testing"

	"webterm-cf/internal/config"
)

func newPathTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.Terminal.WorkingDir = t.TempDir()
	return &Server{cfg: cfg}
}

func TestFilePathConfinesToWorkingDirectory(t *testing.T) {
	server := newPathTestServer(t)
	root := server.cfg.Terminal.WorkingDir

	allowed := map[string]string{
		"":                            root,
		".":                           root,
		"notes.txt":                   filepath.Join(root, "notes.txt"),
		"sub/dir":                     filepath.Join(root, "sub", "dir"),
		root:                          root,
		filepath.Join(root, "a", "b"): filepath.Join(root, "a", "b"),
		"sub/../sibling":              filepath.Join(root, "sibling"),
	}
	for requested, want := range allowed {
		got, err := server.filePath(requested)
		if err != nil {
			t.Fatalf("filePath(%q) failed: %v", requested, err)
		}
		if got != want {
			t.Fatalf("filePath(%q) = %q, want %q", requested, got, want)
		}
	}

	rejected := []string{
		"..",
		"../escape",
		"sub/../../escape",
		"/etc",
		"/etc/passwd",
		filepath.Dir(root),
		filepath.Join(root, "..", "escape"),
	}
	for _, requested := range rejected {
		if got, err := server.filePath(requested); err == nil {
			t.Fatalf("filePath(%q) = %q, want an error", requested, got)
		}
	}
}

func TestWithinRootTreatsSiblingPrefixesAsOutside(t *testing.T) {
	root := filepath.Join(t.TempDir(), "home")
	if !withinRoot(root, root) {
		t.Fatal("root must be inside itself")
	}
	if !withinRoot(root, filepath.Join(root, "child")) {
		t.Fatal("child must be inside root")
	}
	if withinRoot(root, root+"-sibling") {
		t.Fatal("sibling sharing a name prefix must be outside root")
	}
	if withinRoot(root, filepath.Dir(root)) {
		t.Fatal("parent must be outside root")
	}
}
