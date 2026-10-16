# Captured incomplete Responses exchange

A real Copilot `gpt-6-luna` request with `max_output_tokens: 16` returned a valid `response.incomplete` terminal. All request keys, response fields, event order, reasoning-only output, usage, and provider accounting remain in their original shape. Identifiers, opaque encrypted values, cache/safety identifiers, and creation timestamps use stable fixtures. No authentication headers or environment details are included.

The upstream SSE body was reassembled across CPA request-log transport chunk boundaries before JSON decoding. The log appended a transport `context canceled` diagnostic after the terminal; that text is separate from the SSE body. The client body was captured directly. The response consumed 16 output/reasoning tokens and returned no visible answer; the fixture preserves that result.
