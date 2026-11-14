# Native PDF view-tool inference captures

These six fixtures contain genuine forwarded native Copilot CLI inference requests and complete HTTP 200 SSE responses.
The five original captures were recorded on 2026-10-09 UTC, and the sixth captures the subsequent GPT persisted-session follow-up.
The GPT pair captures the initial native `view` call and the next model request containing its paired function call and function output.
The Gemini pair captures the initial native `view` call and the next model request containing its paired assistant tool call and tool message.
The fifth capture is Gemini's same-session text-only extra user turn with the actual preceding tool and answer history retained.
The sixth capture is GPT's real text-only follow-up after its retained native session was loaded through ACP, as independently verified by the evidence owner.
The original first harness incorrectly stopped on an ACP notification with a null name and did not run GPT's extra user turn, while the later persisted-session run completed that separate turn successfully.
The GPT fixture establishes the resulting inference request and response after session loading, while the ACP acceptance exchange remains separate private evidence and is not projected into these inference bodies.
Gemini's extra user turn is same-process continuation, while GPT's extra user turn follows persisted native session loading.

The initial requests retain the original local `tagged_files` route rather than a projected inline PDF attachment.
The actual native view results retain all 623 bytes of the unchanged PDF canary, whose SHA256 is `e8627ac30088e8d989b1e265018ac32bc5b7b94bb4fdb51954deaff0c3ff2d68`.
Those results are raw PDF text produced by the native file reader, including its PDF syntax and Q7B9 marker, rather than output from a substitute parser.
Both tool-result model follow-ups identify Q7B9, and both extra user turns identify it from retained conversation history.
The loaded GPT request retains the original user, native view arguments and call ID, identical PDF result, and prior assistant text before its new user message, which does not contain Q7B9 or a new file reference.
Its exact native model is `gpt-6-luna` with medium reasoning effort, and its new response completes with Q7B9 and zero new tool calls.

Request field topology, options, tool schemas, call/result structure, and content order are preserved.
Private local paths and native resource basenames are replaced consistently under `/sanitized/native-attachments/`.
Instructions, environment text, tool descriptions, and human-readable reasoning text are replaced with explicit placeholders while retaining their fields.
Dynamic identifiers and opaque signed or encrypted blobs use consistent placeholders across all six flows, preserving the equality relationships of calls, results, and history.
The captured GPT client replaces the function item's `id` in its continuation request while retaining the original `call_id`, and the fixtures preserve that distinction and paired result identity.
The loaded GPT request also reflects its actual changed system instructions and rebuilt prior assistant item ID and content fields, retaining prior text while omitting the original response's empty logprobs field.
Those actual client preparation differences are preserved rather than replaced with fabricated identity parity, and newly observed identifiers use distinct placeholders without rewriting the five proven fixtures.
Persistent safety identifiers use the explicit portable string sentinel `SANITIZED_SAFETY_IDENTIFIER_001`.
Streamed arguments and reasoning are sanitized as logical concatenated values before redistribution across the original delta events, retaining every event and its order.
The SSE files retain all original framing and final blank-line delimiters after terminal events or done markers, so patch whitespace warnings must not cause these delimiters to be removed.
Capture headers, credentials, raw private paths, private run identifiers, and actual machine/session identifiers are excluded.
No original private capture is modified.

The tests replay the original native request with only existing model/stream normalization and compare complete native response event fields and order, permitting existing Chat object normalization.
They verify exact native models, terminal states, marker answers, view argument paths, call/result identities, PDF byte hashes, Gemini's real same-process history, and GPT's actual loaded-session history.
There are no derived JSON response files or projected request fixtures in this set.
Sanitized fixtures cannot prove transport fingerprint parity, signed-blob validity, live cross-protocol PDF support, upload behavior, or provider-side document parsing independent of the client's native view tool.
