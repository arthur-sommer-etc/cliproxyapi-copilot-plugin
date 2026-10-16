//go:build !windows

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/translate"
)

type liveAttachmentReasoningTrace struct {
	issued   map[string]bool
	pairs    map[string]bool
	required map[string]bool
	replayed bool
	foreign  bool
	missing  bool
}

func (trace *liveAttachmentReasoningTrace) complete() bool {
	return trace.replayed && !trace.foreign && !trace.missing
}

func (trace *liveAttachmentReasoningTrace) observe(request, response, protocol string) {
	if trace.issued == nil {
		trace.issued = make(map[string]bool)
		trace.pairs = make(map[string]bool)
		trace.required = make(map[string]bool)
	}
	requestPairs := liveAttachmentReasoningPairs(request, protocol, false)
	for pair := range trace.required {
		if !requestPairs[pair] {
			trace.missing = true
		}
	}
	for pair := range requestPairs {
		if !trace.pairs[pair] {
			trace.foreign = true
		} else {
			trace.replayed = true
		}
	}
	for hash := range liveAttachmentReasoningItems(response, protocol, true) {
		trace.issued[hash] = true
	}
	for pair := range liveAttachmentReasoningPairs(response, protocol, true) {
		trace.pairs[pair] = true
		if !strings.HasSuffix(pair, "|") {
			trace.required[pair] = true
		}
	}
}

func liveAttachmentReasoningToolAnchor(items []any) string {
	var calls []string
	add := func(id, name string, arguments map[string]any) {
		if _, decoded, encoded := translate.DecodeClaudeToolIDs(id); encoded {
			id = decoded
		}
		if id != "" && name != "" && arguments != nil {
			encoded, _ := json.Marshal([]string{id, name, liveAttachmentArgumentSHA256(arguments)})
			calls = append(calls, string(encoded))
		}
	}
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["type"] == "function_call" {
			var arguments map[string]any
			if json.Unmarshal([]byte(attachmentString(item, "arguments")), &arguments) == nil {
				add(attachmentString(item, "call_id"), attachmentString(item, "name"), arguments)
			}
		}
		if item["type"] == "tool_use" {
			arguments, _ := item["input"].(map[string]any)
			add(attachmentString(item, "id"), attachmentString(item, "name"), arguments)
		}
		tools, _ := item["tool_calls"].([]any)
		for _, rawTool := range tools {
			tool, _ := rawTool.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			var arguments map[string]any
			if json.Unmarshal([]byte(attachmentString(function, "arguments")), &arguments) == nil {
				add(attachmentString(tool, "id"), attachmentString(function, "name"), arguments)
			}
		}
	}
	if len(calls) == 0 {
		return ""
	}
	sort.Strings(calls)
	encoded, _ := json.Marshal(calls)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func liveAttachmentReasoningPairs(body, protocol string, response bool) map[string]bool {
	pairs := make(map[string]bool)
	add := func(encoded string, items []any, response bool) {
		anchor := liveAttachmentReasoningToolAnchor(items)
		for hash := range liveAttachmentReasoningItems(encoded, protocol, response) {
			pairs[hash+"|"+anchor] = true
		}
	}
	if response {
		var tools []any
		events := attachmentStreamObjects(body)
		for _, event := range events {
			switch protocol {
			case "/responses":
				root := event
				if event["type"] == "response.completed" {
					root, _ = event["response"].(map[string]any)
				}
				if values, ok := root["output"].([]any); ok {
					tools = append(tools, values...)
				}
			case "/v1/messages":
				if event["type"] == "assembled_tool_use" {
					tools = append(tools, event["item"])
				}
				if event["type"] == "message" {
					values, _ := event["content"].([]any)
					tools = append(tools, values...)
				}
			case "/chat/completions":
				choices, _ := event["choices"].([]any)
				for _, raw := range choices {
					choice, _ := raw.(map[string]any)
					if message, ok := choice["message"].(map[string]any); ok {
						tools = append(tools, message)
					}
				}
			}
		}
		if protocol == "/chat/completions" && len(events) > 0 {
			model := attachmentString(events[0], "model")
			if stream, valid := liveChatCompletionStream([]byte(body), model); valid {
				for _, call := range stream.ToolCalls {
					arguments, _ := json.Marshal(call.Arguments)
					tools = append(tools, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": string(arguments)})
				}
			}
		}
		add(body, tools, true)
		return pairs
	}
	var root map[string]any
	if json.Unmarshal([]byte(body), &root) != nil {
		return pairs
	}
	if protocol == "/responses" {
		items, _ := root["input"].([]any)
		var turn []any
		flush := func() {
			encoded, _ := json.Marshal(map[string]any{"input": turn})
			add(string(encoded), turn, false)
			turn = nil
		}
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item["type"] == "function_call_output" {
				flush()
			} else {
				turn = append(turn, raw)
			}
		}
		flush()
	} else {
		messages, _ := root["messages"].([]any)
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			if message["role"] != "assistant" {
				continue
			}
			tools := []any{message}
			if protocol == "/v1/messages" {
				tools, _ = message["content"].([]any)
			}
			encoded, _ := json.Marshal(map[string]any{"messages": []any{message}})
			add(string(encoded), tools, false)
		}
	}
	return pairs
}

func liveAttachmentReasoningItems(body, protocol string, response bool) map[string]bool {
	items := make(map[string]bool)
	add := func(value any) {
		encoded, err := json.Marshal(value)
		if err == nil {
			hash := sha256.Sum256(encoded)
			items[hex.EncodeToString(hash[:])] = true
		}
	}
	content := func(values any) {
		blocks, _ := values.([]any)
		for _, raw := range blocks {
			block, _ := raw.(map[string]any)
			switch block["type"] {
			case "reasoning":
				if encrypted := attachmentString(block, "encrypted_content"); encrypted != "" {
					add(map[string]any{"type": "reasoning", "id": attachmentString(block, "id"), "encrypted_content": encrypted})
				}
			case "thinking":
				if signature := attachmentString(block, "signature"); signature != "" {
					add(map[string]any{"type": "thinking", "thinking": attachmentString(block, "thinking"), "signature": signature})
				}
			case "redacted_thinking":
				if data := attachmentString(block, "data"); data != "" {
					add(map[string]any{"type": "redacted_thinking", "data": data})
				}
			}
		}
	}
	message := func(value map[string]any) {
		if value["role"] != "assistant" {
			return
		}
		if opaque, exists := value["reasoning_opaque"]; exists && opaque != nil && opaque != "" {
			add(opaque)
		}
		content(value["content"])
	}
	if !response {
		var root map[string]any
		if json.Unmarshal([]byte(body), &root) != nil {
			return items
		}
		if protocol == "/responses" {
			content(root["input"])
		} else {
			messages, _ := root["messages"].([]any)
			for _, raw := range messages {
				value, _ := raw.(map[string]any)
				message(value)
			}
		}
		return items
	}
	thinking := make(map[float64]map[string]any)
	for _, event := range attachmentStreamObjects(body) {
		switch protocol {
		case "/responses":
			if event["type"] == "response.completed" {
				root, _ := event["response"].(map[string]any)
				content(root["output"])
			} else if event["object"] == "response" {
				content(event["output"])
			}
		case "/chat/completions":
			choices, _ := event["choices"].([]any)
			for _, raw := range choices {
				choice, _ := raw.(map[string]any)
				for _, field := range []string{"delta", "message"} {
					value, _ := choice[field].(map[string]any)
					if opaque, exists := value["reasoning_opaque"]; exists && opaque != nil && opaque != "" {
						add(opaque)
					}
				}
			}
		case "/v1/messages":
			if event["type"] == "message" {
				message(event)
			}
			index, _ := event["index"].(float64)
			switch event["type"] {
			case "content_block_start":
				block, _ := event["content_block"].(map[string]any)
				if block["type"] == "thinking" || block["type"] == "redacted_thinking" {
					thinking[index] = block
				}
			case "content_block_delta":
				if block := thinking[index]; block != nil {
					delta, _ := event["delta"].(map[string]any)
					field := map[string]string{"thinking_delta": "thinking", "signature_delta": "signature"}[attachmentString(delta, "type")]
					if field != "" {
						block[field] = attachmentString(block, field) + attachmentString(delta, field)
					}
				}
			case "content_block_stop":
				if block := thinking[index]; block != nil {
					content([]any{block})
					delete(thinking, index)
				}
			}
		}
	}
	return items
}

func TestAttachmentReasoningEachToolTurnRequiresItsOwnContinuation(t *testing.T) {
	turn := func(protocol, token, call string, response bool) string {
		var root map[string]any
		if protocol == "/responses" {
			items := []any{map[string]any{"type": "reasoning", "id": "rs_" + token, "encrypted_content": token}, map[string]any{"type": "function_call", "id": "fc_" + call, "call_id": call, "name": "exec_command", "arguments": "{}"}}
			if response {
				root = map[string]any{"object": "response", "output": items}
			} else {
				root = map[string]any{"input": append(items, map[string]any{"type": "function_call_output", "call_id": call, "output": "ok"})}
			}
		} else {
			message := map[string]any{"role": "assistant"}
			if protocol == "/v1/messages" {
				message["type"] = "message"
				message["content"] = []any{map[string]any{"type": "thinking", "thinking": "synthetic thought", "signature": token}, map[string]any{"type": "tool_use", "id": call, "name": "exec_command", "input": map[string]any{}}}
			} else {
				message["reasoning_opaque"] = token
				message["tool_calls"] = []any{map[string]any{"id": call, "type": "function", "function": map[string]any{"name": "exec_command", "arguments": "{}"}}}
			}
			if !response {
				root = map[string]any{"messages": []any{message}}
			} else if protocol == "/v1/messages" {
				root = message
			} else {
				root = map[string]any{"choices": []any{map[string]any{"message": message}}}
			}
		}
		encoded, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	for _, protocol := range []string{"/responses", "/chat/completions", "/v1/messages"} {
		t.Run(protocol, func(t *testing.T) {
			requestA, requestB := turn(protocol, "owned-A", "render", false), turn(protocol, "owned-B", "view", false)
			var first, second map[string]any
			if json.Unmarshal([]byte(requestA), &first) != nil || json.Unmarshal([]byte(requestB), &second) != nil {
				t.Fatal("invalid turn fixture")
			}
			field := "messages"
			if protocol == "/responses" {
				field = "input"
			}
			first[field] = append(first[field].([]any), second[field].([]any)...)
			exact, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			for _, final := range []struct {
				body  string
				valid bool
			}{{string(exact), true}, {requestA, false}} {
				trace := liveAttachmentReasoningTrace{}
				trace.observe(`{}`, turn(protocol, "owned-A", "render", true), protocol)
				trace.observe(requestA, turn(protocol, "owned-B", "view", true), protocol)
				trace.observe(final.body, `{}`, protocol)
				if trace.complete() != final.valid {
					t.Fatal("later carrier was lost or exact multi-turn reasoning was rejected")
				}
			}
			wrong := liveAttachmentReasoningTrace{}
			wrong.observe(`{}`, turn(protocol, "owned-A", "render", true), protocol)
			wrong.observe(turn(protocol, "owned-A", "view", false), `{}`, protocol)
			if wrong.complete() {
				t.Fatal("owned reasoning attached to a different tool continuation passed")
			}
		})
	}
}

func TestAttachmentReasoningReplayRequiresPreviouslyIssuedSameFormatValue(t *testing.T) {
	fixtures := []struct{ protocol, response, request string }{
		{"/chat/completions", `{"choices":[{"message":{"reasoning_opaque":{"ciphertext":"synthetic-owned"}}}]}`, `{"messages":[{"role":"assistant","reasoning_opaque":{"ciphertext":"synthetic-owned"}}]}`},
		{"/responses", `{"object":"response","output":[{"type":"reasoning","id":"rs_owned","encrypted_content":"synthetic-owned"}]}`, `{"input":[{"type":"reasoning","id":"rs_owned","encrypted_content":"synthetic-owned"}]}`},
		{"/v1/messages", `{"type":"message","role":"assistant","content":[{"type":"thinking","thinking":"synthetic thought","signature":"synthetic-owned"}]}`, `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"synthetic thought","signature":"synthetic-owned"}]}]}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.protocol, func(t *testing.T) {
			trace := liveAttachmentReasoningTrace{}
			trace.observe(`{}`, fixture.response, fixture.protocol)
			trace.observe(fixture.request, `{}`, fixture.protocol)
			if len(trace.issued) != 1 || !trace.replayed || trace.foreign {
				t.Fatal("issued reasoning lost same-format replay proof")
			}
			foreign := liveAttachmentReasoningTrace{}
			foreign.observe(`{}`, fixture.response, fixture.protocol)
			foreign.observe(strings.ReplaceAll(fixture.request, "synthetic-owned", "synthetic-foreign"), `{}`, fixture.protocol)
			if !foreign.foreign || foreign.replayed {
				t.Fatal("foreign reasoning established provider ownership")
			}
		})
	}
}
