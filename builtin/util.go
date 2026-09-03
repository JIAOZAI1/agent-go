package builtin

import (
	"os"
	"strconv"
)

// truncateBytes truncates s at n bytes of content, appending a marker when
// trimming occurred. n<=0 means no truncation.
func truncateBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "\n[…] truncated at " + strconv.Itoa(n) + " bytes"
}

// preview truncates a human-facing preview text to at most n bytes.
func preview(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "\n[…] preview truncated"
}

// existingPerm returns the permission bits an existing file should keep when
// edited, or 0o644 for a brand-new file created by an edit append.
func existingPerm(path string) os.FileMode {
	if fi, err := os.Stat(path); err == nil {
		return fi.Mode().Perm()
	}
	return 0o644
}
