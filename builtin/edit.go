package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JIAOZAI1/agent-go/tool"
)

// editTool replaces or appends text inside a file confined to the root.
type editTool struct {
	Config
	root string
}

// editArgs is the accepted tool argument object.
type editArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// NewEdit returns a tool that performs a single textual replacement in a file
// under cfg.Root. Regulatory explicit-registration note applies as with read.
func NewEdit(cfg Config) tool.Tool {
	root, _ := cfg.root()
	return &editTool{Config: cfg, root: root}
}

func (t *editTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "edit",
		Description: "在配置根目录内的文件里精确替换一处文本。old 为空表示把 new 追加到文件末尾；若 old 在文件中不存在，文件保持不变并返回提示。path 必须在根目录内。",
		Parameters: json.RawMessage(`{
			"type":"object",
			"properties":{
				"path":{"type":"string"},
				"old":{"type":"string"},
				"new":{"type":"string"}
			},
			"required":["path"]
		}`),
	}
}

func (t *editTool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if err := t.configErr(); err != nil {
		return tool.Result{}, err
	}
	if ctx == nil {
		return tool.Result{}, errors.New("builtin edit: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var args editArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return tool.Result{}, fmt.Errorf("edit: %w", err)
	}
	path, err := resolveInRoot(t.root, args.Path)
	if err != nil {
		return tool.Result{}, fmt.Errorf("edit: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// allow append-to-new file
			if args.Old == "" {
				data = nil
			} else {
				return tool.Result{}, fmt.Errorf("edit: target file not found: %w", err)
			}
		} else {
			return tool.Result{}, fmt.Errorf("edit: read: %w", err)
		}
	}
	content := string(data)

	var result string
	switch {
	case args.Old == "":
		content += args.New // append
		result = "appended text to " + filepath.Base(path)
	default:
		idx := strings.Index(content, args.Old)
		if idx < 0 {
			return tool.Result{Content: "old text not found; file unchanged"}, nil
		}
		content = content[:idx] + args.New + content[idx+len(args.Old):]
		result = "replaced one occurrence in " + filepath.Base(path)
	}

	if err := atomicWriteFile(path, []byte(content)); err != nil {
		return tool.Result{}, fmt.Errorf("edit: write: %w", err)
	}
	cfg := t.withDefaults()
	return tool.Result{Content: result + "\n" + preview(content, cfg.EditOutputLen)}, nil
}

// configErr is a minimal root presence guard reused across fs tools.
func (t *editTool) configErr() error {
	if t.root == "" {
		return errors.New("builtin edit: invalid or missing Root")
	}
	return nil
}

// atomicWriteFile writes data to path via a same-dir temp file + rename so the
// target is never observed half-written (and any source file stays intact on
// failure).
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".edit-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		_ = os.Remove(tmp) // best-effort cleanup unless renamed away
	}()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, existingPerm(path)); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
