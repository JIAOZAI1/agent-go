package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/JIAOZAI1/agent-go/tool"
)

// shellTool runs a command through the configured shell.
type shellTool struct {
	Config
	root string
}

// shellArgs is the accepted tool argument object.
type shellArgs struct {
	Command   string `json:"command"`
	Workdir   string `json:"workdir,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"`
}

// NewBash returns a tool that runs a shell command via cfg.Shell (default
// "/bin/sh -c") from a working directory bounded to cfg.Root/WORKDir.
//
// This is NOT an OS-level sandbox: it only defaults the working directory to
// Root and the real isolation for arbitrary commands requires a container or
// another OS mechanism. Registering a Bash tool (or not) is the caller's
// decision, and untrusted models should not receive it except under a real
// sandbox.
func NewBash(cfg Config) tool.Tool {
	root, _ := cfg.root()
	return &shellTool{Config: cfg, root: root}
}

func (t *shellTool) Spec() tool.Spec {
	return tool.Spec{
		Name: "bash",
		Description: "在配置根目录内的工作目录用 shell 执行一条命令，返回合并的标准输出/" +
			"错误与退出码。可处理任意命令（非沙箱），是否授予应用方决定。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"command":{"type":"string"},
				"workdir":{"type":"string"},
				"timeout_ms":{"type":"integer","minimum":1}
			},
			"required":["command"]
		}`),
	}
}

func (t *shellTool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if t.root == "" {
		return tool.Result{}, errors.New("builtin bash: invalid or missing Root")
	}
	if ctx == nil {
		return tool.Result{}, errors.New("builtin bash: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args shellArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return tool.Result{}, fmt.Errorf("bash: %w", err)
	}
	if args.Command == "" {
		return tool.Result{}, errors.New("builtin bash: empty command")
	}

	timeout := time.Duration(defaultShellTimeoutMs) * time.Millisecond
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
	}
	execCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	dir := t.resolveDir(args.Workdir)
	cmd := exec.CommandContext(execCtx, t.cfgShell(), "-c", args.Command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), t.Env...)

	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	body := labelOutputs(stdout.String(), stderr.String())

	switch {
	case errors.Is(execCtx.Err(), context.DeadlineExceeded):
		return tool.Result{}, fmt.Errorf("%w: bash: timed out after %s", context.DeadlineExceeded, timeout)
	case errors.Is(execCtx.Err(), context.Canceled):
		return tool.Result{}, execCtx.Err()
	case err == nil:
		return tool.Result{Content: "exit=0\n" + body}, nil
	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return tool.Result{Content: "exit=" + strconv.Itoa(exitErr.ExitCode()) + "\n" + body}, nil
		}
		return tool.Result{}, fmt.Errorf("bash: %w", err)
	}
}

// boundedBuffer caps how many bytes are retained; bytes beyond limit are
// dropped so a chatty process cannot exhaust memory.
type boundedBuffer struct {
	b bytes.Buffer
}

// Write appends p, dropping overflow.
func (b *boundedBuffer) Write(p []byte) (int, error) {
	room := defaultMaxShellOutput - b.b.Len()
	if room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		_, _ = b.b.Write(p)
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return b.b.String() }

// resolveDir computes the working directory for the shell from the request's
// optional workdir argument, confined to cfg.Root.
func (t *shellTool) resolveDir(workdirArg string) string {
	if workdirArg == "" {
		if t.Config.WorkDir != "" {
			abs := t.Config.WorkDir
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(t.root, abs)
			}
			if within(t.root, abs) {
				return abs
			}
		}
		return t.root
	}
	wd := workdirArg
	if !filepath.IsAbs(wd) {
		wd = filepath.Join(t.root, wd)
	}
	if within(t.root, wd) {
		return wd
	}
	return t.root
}

// labelOutputs formats stdout and stderr with OUT:/ERR: labels. Each side is
// trimmed so the combined offset and labels stay under a sane bound.
func labelOutputs(out, errS string) string {
	out = capText(out, defaultMaxShellOutput)
	errS = capText(errS, defaultMaxShellOutput)

	var b bytes.Buffer
	b.WriteString("OUT:\n")
	b.WriteString(withTrailingNL(out))
	b.WriteString("ERR:\n")
	b.WriteString(withTrailingNL(errS))

	// Remove one unused trailing newline introduced when both are empty for a
	// tidier empty result.
	if s := b.String(); s == "OUT:\nERR:\n" {
		return s[:len(s)-1]
	}
	return b.String()
}

// withTrailingNL ensures a non-empty block ends in a newline.
func withTrailingNL(s string) string {
	if s == "" {
		return ""
	}
	if s[len(s)-1] != '\n' {
		return s + "\n"
	}
	return s
}

// capText limits s to max+label bytes (approximation; good enough for bounds).
func capText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] // caller adds truncation note on overflow later if any
}
