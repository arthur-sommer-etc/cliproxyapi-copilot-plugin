# Copilot Thinking Recovery

Copilot Chat Completions can return `reasoning_text` and `reasoning_opaque` alongside ordinary assistant content and function calls.
The bridge preserves that pair when serving Messages or Responses clients, including `gemini-3.8-flash` on an authenticated Chat endpoint.
Endpoint discovery remains authoritative and this conversion does not advertise additional provider endpoints.

## Recovery Contract

Responses carries a bridge replay value in a reasoning item's `encrypted_content` field.
Messages carries it in a thinking block's `signature` field.
The value is an authenticated bridge carrier, not a native Claude signature or Google thought signature.
It contains the original assistant content, function calls and Copilot reasoning fields, plus a digest of the preceding transcript.
Base64 encoding does not encrypt these values.
Treat the carrier as private conversation data and retain it unchanged in the next request.

Replay verifies the selected account, persistent OAuth credential, model, API endpoint and available conversation headers.
When conversation headers are absent, the complete preceding transcript still binds replay.
The bridge compares ordinary assistant content and tool calls before restoring the original provider message.
Changed history, thinking text, arguments, call identifiers, credential or route returns a translation error before inference.
Foreign signed reasoning and unsigned thinking history that lacks a recovery carrier are rejected on Chat conversion.
The bridge does not silently discard those blocks or reinterpret foreign signatures.

Ordinary Copilot bearer refresh preserves the carrier because it does not change the persistent OAuth credential.
A changed API origin cannot receive restored reasoning during automatic retry.
Replacing the persistent OAuth credential invalidates existing carriers and requires a new conversation.
Recreating the plugin with the same persistent credential preserves stateless recovery.

The executor reads the original client request when available.
The host-translated payload may already have discarded signatures and cannot recover that lost state.
Ordinary request and response fields continue to use the existing CLIProxyAPI translators.

## Streaming

The recorded Gemini arrangement places `reasoning_text` on a role chunk and `reasoning_opaque` on the finish chunk.
The bridge accumulates assistant and parallel-tool state while preserving ordinary output before the first Copilot thinking field.
From that field onward, it buffers the remaining stream until DONE, transport EOF and successful stream closure.
This delay lets the bridge attach the late opaque value before closing the corresponding thinking block.
Converted Chat streams have an 8 MiB wire budget.
Overflow, malformed trailing frames, missing termination and transport failures prevent successful completion.
Messages tool blocks are emitted sequentially even when upstream function arguments arrive in parallel.

## Earlier Attempt

The read-only history in [cloudflared-opencode #28](https://github.com/hak-trio/cloudflared-opencode/issues/28) identifies losses before downstream translation.
LiteLLM streaming filters and serializers discarded the Copilot fields before response callbacks could recover them.
Non-streaming retained provider-specific fields, but the downstream Messages translator did not restore their replay semantics.
[PR #29](https://github.com/hak-trio/cloudflared-opencode/pull/29) selected native Messages configuration for Claude.
[PR #31](https://github.com/hak-trio/cloudflared-opencode/pull/31) and [PR #33](https://github.com/hak-trio/cloudflared-opencode/pull/33) clarified the limitations and artifact evidence.
Those changes did not implement Gemini recovery.
Historical Gemini probes accepted Chat Completions and rejected the tested integration's Responses and Messages endpoints.

The bridge owns the Copilot dialect at the provider boundary, before generic translation loses it.
Offline structured tests cover JSON, streamed role and finish chunks, exact later requests, parallel tools and rejected replay changes.
Live Copilot and Gemini inference, original-client signature carriage and server-side reasoning reuse remain NOT RUN.
[Issue #57](https://github.com/ririnto/cpa-copilot-bridge/issues/57) tracks that external acceptance.
