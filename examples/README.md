# Examples

Turnkey `go run` examples of composing `agent-go`'s public packages. They run
offline (no model provider and no API key) so they can be executed in CI and by
any developer immediately.

| Directory | Demonstrates |
| --- | --- |
| `examples/weather_agent` | A tool-calling `ToolLoopAgent`: session store, tool catalog + middleware, `FanoutSink` lifecycle events, and `RunStats`. |

Run one with:

```bash
go run ./examples/weather_agent
```

Each example keeps a small fake `model.Executor` stand-in so it needs no network
or credentials. Because the rest of the wiring only depends on the public
`agent`/`model`/`message`/`tool`/`session` contracts, swapping a fake for a real
provider (e.g. `provider/openai`) changes just the executor construction.
