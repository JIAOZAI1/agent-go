// Package trim reduces an over-budget conversation history to a safe working
// set before it is sent to a model. It operates on a per-run snapshot only and
// never writes back to a session store: trimming, summarization and compaction
// policies live here, isolated from the session.Store contract so a future
// persistence backend does not have to care how the model-facing history is
// kept within its context window.
//
// The package is dependency-light on purpose: it imports only the message
// types it inspects, so an application may add additional redaction or
// estimation wrappers without pulling in provider or agent code.
package trim
