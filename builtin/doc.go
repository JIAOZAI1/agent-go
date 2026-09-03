// Package builtin provides a small set of prewritten, provider-independent
// tools for reading/editing files and executing a command, which an application
// can explicitly register with a tool.Builder before exposing them to an Agent.
//
// Registration is always explicit and manual: nothing in this package attaches
// a tool to any Agent automatically. To let an agent read, edit, or run
// commands, you call builder.AddTool yourself and bound every tool to an
// allowed root:
//
//	b := tool.NewBuilder()
//	_ = b.AddTool(builtin.NewRead(builtin.Config{Root: "/srv/proj"}))
//	_ = b.AddTool(builtin.NewEdit(builtin.Config{Root: "/srv/proj"}))
//	tools, _ := b.Build()
//
// Only tools you Add become part of an agent's capability set; that explicit
// Add is where the caller decides what access is being granted.
//
// Safety contract:
//   - read/edit confine all access to cfg.Root (path containment incl. symlink
//     resolution), rejecting "../" or absolute paths that escape Root.
//   - bash runs with Root (or cfg.WorkDir) as its working directory, but does
//     NOT provide an OS-level sandbox: a command may cd elsewhere or otherwise
//     act on the whole machine. Real isolation must come from the OS/container,
//     so exposing NewBash to an untrusted model is the caller's decision.
//   - Outputs are size-bounded and replies never echo credentials.
//
// The package depends only on the standard library and on agent-go/tool.
package builtin
