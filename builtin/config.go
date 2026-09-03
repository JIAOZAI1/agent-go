package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Default limits used when the corresponding Config field is left at its zero
// value. They keep file/command outputs from blowing up an agent context.
const (
	// Keep read/edit/bash outputs from blowing up an agent context.
	defaultMaxReadBytes   = 1 << 20      // 1 MiB
	defaultEditSummaryLen = 1 << 10      // 1 KiB
	defaultShellTimeoutMs = int64(60000) // 60 s
	defaultMaxShellOutput = 64 << 10     // 64 KiB cumulative stdout+stderr
)

// ErrOutsideRoot indicates a tool was asked to operate on a path outside the
// configured Root (via an absolute path, "..", or a symlink escape).
var ErrOutsideRoot = errors.New("builtin: path outside allowed root")

// Config bounds the file-system and command access a builtin tool may reach. It
// is captured when a tool is constructed; the same *builtin tool values may be
// used concurrently from many goroutines.
type Config struct {
	// Root is the directory tree that read/edit confine access to and the
	// default working directory for bash. It must name an existing directory;
	// tools built with an empty or invalid Root report an error when run.
	Root string

	// MaxReadBytes is the read tool's single-file limit in bytes. 0 uses 1 MiB.
	MaxReadBytes int64

	// EditOutputLen is the maximum length of the text preview edit returns.
	// 0 uses 1 KiB.
	EditOutputLen int

	// Shell is the interpreter executable bash runs with "-c". Empty uses a
	// POSIX "/bin/sh". Windows hosts should set an available POSIX shell.
	Shell string

	// WorkDir is the directory bash runs in. Empty defaults to Root.
	WorkDir string

	// Env is an optional list of extra "KEY=VALUE" entries appended to the
	// process environment for bash.
	Env []string
}

// withDefaults returns c with zero-valued limits replaced by their defaults.
func (c Config) withDefaults() Config {
	if c.MaxReadBytes <= 0 {
		c.MaxReadBytes = defaultMaxReadBytes
	}
	if c.EditOutputLen <= 0 {
		c.EditOutputLen = defaultEditSummaryLen
	}
	return c
}

// root validates cfg.Root, returning it absolute and normalized.
func (c Config) root() (string, error) {
	if strings.TrimSpace(c.Root) == "" {
		return "", errors.New("builtin: empty Root")
	}
	abs, err := filepath.Abs(c.Root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", errors.New("builtin: Root is not a directory")
	}
	return abs, nil
}

// resolveInRoot returns a path for requestPath that stays confined inside root,
// rejecting absolute escape, "..", and symlinks that point outside root. Paths
// that already exist on disk are resolved through symlinks before the check.
func resolveInRoot(root, requestPath string) (string, error) {
	if strings.TrimSpace(requestPath) == "" {
		return "", errors.New("builtin: empty path")
	}
	var resolved string
	if filepath.IsAbs(requestPath) {
		resolved = filepath.Clean(requestPath)
	} else {
		resolved = filepath.Join(root, requestPath)
	}
	if !within(root, resolved) {
		return "", ErrOutsideRoot
	}
	if real, err := filepath.EvalSymlinks(resolved); err == nil {
		if !within(root, real) {
			return "", ErrOutsideRoot
		}
		resolved = real
	}
	return resolved, nil
}

// within reports whether p equals root or is strictly under root.
func within(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

// cfgShell is the interpreter executed by bash.
func (c Config) cfgShell() string {
	if strings.TrimSpace(c.Shell) != "" {
		return c.Shell
	}
	return "/bin/sh"
}

// workDir returns the directory bash should run in.
func (c Config) workDir(root string) string {
	if strings.TrimSpace(c.WorkDir) != "" {
		// Resolve relative WorkDir against Root for confinement.
		if filepath.IsAbs(c.WorkDir) {
			if within(root, c.WorkDir) {
				return c.WorkDir
			}
			return root
		}
		return filepath.Join(root, c.WorkDir)
	}
	return root
}
