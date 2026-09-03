package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/JIAOZAI1/agent-go/tool"
)

// readTool reads a file confined to the configured root.
type readTool struct {
	Config
	root string
}

// readArgs is the object accepted as tool arguments.
type readArgs struct {
	Path string `json:"path"`
}

// NewRead returns a tool that reads a UTF-8 text file confined to cfg.Root.
//
// Like the other builtin tools it is inert until explicitly registered:
//
//	b := tool.NewBuilder()
//	_ = b.AddTool(builtin.NewRead(builtin.Config{Root: projectDir}))
func NewRead(cfg Config) tool.Tool {
	root, _ := cfg.root()
	return &readTool{Config: cfg, root: root}
}

func (t *readTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "read",
		Description: "读取配置根目录内的文本文件内容。path 必须在所允许的根目录内。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
	}
}

func (t *readTool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if err := t.configError(); err != nil {
		return tool.Result{}, err
	}
	if ctx == nil {
		return tool.Result{}, errors.New("builtin read: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args readArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return tool.Result{}, fmt.Errorf("read: %w", err)
	}
	path, err := resolveInRoot(t.root, args.Path)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return tool.Result{}, fmt.Errorf("read: %w", err)
	}
	cfg := t.withDefaults()
	return tool.Result{Content: truncateBytes(string(data), int(cfg.MaxReadBytes))}, nil
}

// configError returns a stable error when the tool was built with a missing or
// invalid Root; nil otherwise. Keeping validation here (rather than at build)
// allows the ergonomic New*(-Config) registration shape and surfaces the
// problem at the first call.
func (t *readTool) configError() error {
	if t.root == "" {
		return errors.New("builtin read: invalid or missing Root")
	}
	return nil
}
