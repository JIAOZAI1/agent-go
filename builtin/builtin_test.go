package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInRoot(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "a.txt")
	_ = os.WriteFile(inside, []byte("x"), 0o644)
	outside := filepath.Join(t.TempDir(), "b.txt")
	_ = os.WriteFile(outside, []byte("y"), 0o644)
	symlink := filepath.Join(dir, "link")
	canSymlink := os.Symlink(outside, symlink) == nil
	if canSymlink {
		t.Cleanup(func() { _ = os.Remove(symlink) })
	}

	tests := []struct {
		name string
		path string
		want error
	}{
		{name: "relative inside", path: "a.txt"},
		{name: "absolute inside", path: inside},
		{name: "dotdot", path: filepath.Join(dir, "..", filepath.Base(outside)), want: ErrOutsideRoot},
		{name: "empty", path: "", want: errAny},
	}
	if canSymlink {
		tests = append(tests, struct {
			name string
			path string
			want error
		}{name: "symlink escape", path: "link", want: ErrOutsideRoot})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveInRoot(dir, tc.path)
			if tc.path == "" {
				if err == nil {
					t.Fatalf("empty path: want error, got nil")
				}
				return
			}
			if tc.want == errAny {
				if err == nil {
					t.Fatalf("expected an error for %q, got nil", tc.path)
				}
				return
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("resolveInRoot(%q) error = %v, want %v", tc.path, err, tc.want)
			}
			if tc.want == nil && err != nil {
				t.Fatalf("resolveInRoot(%q) unexpected error: %v", tc.path, err)
			}
		})
	}
}

// errAny is a sentinel meaning "some non-nil error" for empty-path cases.
var errAny = errors.New("any")

func TestReadOK(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "greet.txt")
	_ = os.WriteFile(file, []byte("hello world"), 0o644)

	tool := NewRead(Config{Root: dir})
	raw, _ := json.Marshal(map[string]string{"path": "greet.txt"})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.Content != "hello world" {
		t.Fatalf("content = %q, want 'hello world'", res.Content)
	}
}

func TestReadOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(other, []byte("secret"), 0o644)

	tool := NewRead(Config{Root: dir})
	raw, _ := json.Marshal(map[string]string{"path": other}) // absolute outside root
	_, err := tool.Execute(context.Background(), raw)
	if !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("Execute(outside) error = %v, want ErrOutsideRoot", err)
	}
}

func TestReadMissingRoot(t *testing.T) {
	tool := NewRead(Config{})
	raw, _ := json.Marshal(map[string]string{"path": "x"})
	_, err := tool.Execute(context.Background(), raw)
	if err == nil {
		t.Fatal("Execute with empty Root has no error")
	}
}

func TestEditReplaceAndAppendAndAtomics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "doc.txt")
	_ = os.WriteFile(file, []byte("foo bar baz"), 0o644)

	// replace first occurrence of "bar"
	et := NewEdit(Config{Root: dir, EditOutputLen: 2048})
	spec := et.Spec()
	if spec.Name != "edit" {
		t.Fatalf("spec name = %q, want 'edit'", spec.Name)
	}
	payload := map[string]string{"path": "doc.txt", "old": "bar", "new": "BAR"}
	raw, _ := json.Marshal(payload)
	res, err := et.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("edit replace error = %v", err)
	}
	_ = res
	content, _ := os.ReadFile(file)
	if string(content) != "foo BAR baz" {
		t.Fatalf("after replace content = %q, want 'foo BAR baz'", content)
	}

	// append (old == "")
	payload = map[string]string{"path": "doc.txt", "old": "", "new": "!"}
	raw, _ = json.Marshal(payload)
	if _, err := et.Execute(context.Background(), raw); err != nil {
		t.Fatalf("edit append error = %v", err)
	}
	content, _ = os.ReadFile(file)
	if string(content) != "foo BAR baz!" {
		t.Fatalf("after append content = %q, want 'foo BAR baz!'", content)
	}

	// no temp leftover
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".edit-") {
			t.Fatalf("leftover temp file %q", e.Name())
		}
	}
}

func TestEditOldNotFoundReturnsPrompt(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "d.txt")
	_ = os.WriteFile(file, []byte("abc"), 0o644)

	tool := NewEdit(Config{Root: dir})
	raw, _ := json.Marshal(map[string]string{"path": "d.txt", "old": "zzz", "new": "q"})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("edit when old missing returned error, want prompt text: %v", err)
	}
	if !strings.Contains(res.Content, "not found") {
		t.Fatalf("prompt = %q, want '... not found ...'", res.Content)
	}
	// file unchanged
	c, _ := os.ReadFile(file)
	if string(c) != "abc" {
		t.Fatalf("file changed unexpectedly: %q", c)
	}
}

func TestBashRunInsideRoot(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh on this host")
	}
	dir := t.TempDir()
	tool := NewBash(Config{Root: dir})
	raw, _ := json.Marshal(map[string]string{"command": "pwd"})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("bash error = %v", err)
	}
	want := strings.TrimSuffix(res.Content, "\n")
	if !strings.Contains(want, "OUT:") {
		t.Fatalf("bash output missing OUT label: %q", res.Content)
	}
	// pwd printed should reference dir
	if !strings.Contains(res.Content, filepath.Base(dir)) {
		t.Fatalf("pwd not under root: %q", res.Content)
	}
}

func TestBashExitCodeReportedAsContent(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	tool := NewBash(Config{Root: dir})
	raw, _ := json.Marshal(map[string]string{"command": "exit 3"})
	res, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("bash nonzero should be content not error: %v", err)
	}
	if !strings.HasPrefix(res.Content, "exit=3") {
		t.Fatalf("content prefix = %q, want 'exit=3'", res.Content)
	}
}

func TestBashTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	tool := NewBash(Config{Root: dir})
	raw, _ := json.Marshal(map[string]any{"command": "sleep 5", "timeout_ms": 200})
	_, err := tool.Execute(context.Background(), raw)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bash timeout error = %v, want DeadlineExceeded", err)
	}
}

func TestBashCancelledContext(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := NewBash(Config{Root: dir})
	raw, _ := json.Marshal(map[string]string{"command": "echo hi"})
	_, err := tool.Execute(ctx, raw)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("bash cancelled error = %v, want context.Canceled", err)
	}
}

func TestBashEmptyRoot(t *testing.T) {
	tool := NewBash(Config{})
	raw, _ := json.Marshal(map[string]string{"command": "echo hi"})
	if _, err := tool.Execute(context.Background(), raw); err == nil {
		t.Fatal("bash with empty Root has no error")
	}
}

func TestConfigShellWithNonPOSIXDefaultNotNeeded(t *testing.T) {
	// Just ensure configurable shell plumbing exists without invoking.
	c := Config{Shell: "/bin/bash"}.cfgShell()
	if c != "/bin/bash" {
		t.Fatalf("cfgShell = %q, want /bin/bash", c)
	}
}
