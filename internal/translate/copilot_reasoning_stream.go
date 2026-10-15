package translate

import (
	"context"
	"encoding/json"
	"fmt"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const maxCopilotReasoningStreamBytes = 8 << 20

// CopilotChatStream retains a thinking suffix until late opaque state is available at transport EOF.
type CopilotChatStream struct {
	responses     any
	claude        any
	frames        [][]byte
	size          int
	buffered      bool
	done          bool
	sawFinish     bool
	finishReason  string
	reasoningSent bool
	finished      bool
	failure       error
	sequence      int
	message       map[string]any
	tools         []any
	carrier       string
	signed        bool
	blockFrames   map[int][][]byte
	blockOrder    []int
	blockDone     map[int]bool
}

// Stream preserves ordinary streaming latency and buffers from the first Copilot thinking field.
func (r *CopilotReplay) Stream(ctx context.Context, destination, model string, original, translated, frame []byte, state *CopilotChatStream) ([][]byte, error) {
	if state.failure != nil {
		return nil, state.failure
	}
	out, err := r.chatStream(ctx, destination, model, original, translated, frame, state)
	if err != nil {
		state.failure = err
	}
	return out, err
}

func (r *CopilotReplay) chatStream(ctx context.Context, destination, model string, original, translated, frame []byte, state *CopilotChatStream) ([][]byte, error) {
	_, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, fmt.Errorf("%w: malformed stream frame", ErrCopilotReasoning)
	}
	if state.finished || state.done && (done || len(data) > 0) {
		return nil, ErrCopilotReasoning
	}
	state.size += len(frame)
	if state.size > maxCopilotReasoningStreamBytes {
		return nil, ErrCopilotReasoning
	}
	if len(data) > 0 {
		root, err := decodeObject(data)
		if err != nil || root["error"] != nil {
			return nil, ErrCopilotReasoning
		}
		choices, _ := root["choices"].([]any)
		for _, raw := range choices {
			choice := objectValue(raw)
			delta := objectValue(choice["delta"])
			if hasCopilotReasoning(delta) {
				state.buffered = true
			}
			if len(choices) != 1 || numberKey(choice["index"]) != "0" {
				return nil, ErrCopilotReasoning
			}
			if err := state.accumulate(delta); err != nil {
				return nil, err
			}
			if reason := stringValue(choice["finish_reason"]); reason != "" {
				state.sawFinish = true
				state.finishReason = reason
			}
		}
	}
	state.done = state.done || done
	if state.buffered {
		state.frames = append(state.frames, append([]byte(nil), frame...))
		return nil, nil
	}
	return state.translate(ctx, destination, model, original, translated, frame)
}

func (s *CopilotChatStream) accumulate(delta map[string]any) error {
	if s.message == nil {
		s.message = map[string]any{"role": "assistant", "content": nil}
	}
	if role := stringValue(delta["role"]); role != "" && role != "assistant" {
		return ErrCopilotReasoning
	}
	for _, field := range []string{"content", "reasoning_text"} {
		if value, exists := delta[field]; exists && value != nil {
			text, ok := value.(string)
			if !ok {
				return ErrCopilotReasoning
			}
			s.message[field] = rawStringValue(s.message[field]) + text
		}
	}
	if opaque, exists := delta["reasoning_opaque"]; exists && opaque != nil {
		if prior, exists := s.message["reasoning_opaque"]; exists && replayDigest(prior) != replayDigest(opaque) {
			return ErrCopilotReasoning
		}
		s.message["reasoning_opaque"] = opaque
	}
	calls, _ := delta["tool_calls"].([]any)
	for _, raw := range calls {
		call := objectValue(raw)
		index := int(int64Value(call["index"]))
		if index < 0 || index > 1024 {
			return ErrCopilotReasoning
		}
		for len(s.tools) <= index {
			s.tools = append(s.tools, nil)
		}
		if s.tools[index] == nil {
			s.tools[index] = map[string]any{"type": "function", "function": map[string]any{}}
		}
		tool := objectValue(s.tools[index])
		if id := stringValue(call["id"]); id != "" {
			if prior := stringValue(tool["id"]); prior != "" && prior != id {
				return ErrCopilotReasoning
			}
			tool["id"] = id
		}
		function := objectValue(call["function"])
		target := objectValue(tool["function"])
		for _, field := range []string{"name", "arguments"} {
			if value, exists := function[field]; exists {
				text, ok := value.(string)
				if !ok {
					return ErrCopilotReasoning
				}
				target[field] = rawStringValue(target[field]) + text
			}
		}
	}
	return nil
}

// FinishStream releases complete reasoning only after the host reports transport EOF.
func (r *CopilotReplay) FinishStream(ctx context.Context, destination, model string, original, translated []byte, state *CopilotChatStream) ([][]byte, error) {
	if state.failure != nil {
		return nil, state.failure
	}
	if !state.buffered {
		return nil, nil
	}
	if !state.done || !state.sawFinish || state.finished {
		return nil, ErrCopilotReasoning
	}
	if len(state.tools) > 0 {
		for _, raw := range state.tools {
			tool := objectValue(raw)
			if stringValue(tool["id"]) == "" || stringValue(objectValue(tool["function"])["name"]) == "" {
				return nil, ErrCopilotReasoning
			}
		}
		state.message["tool_calls"] = state.tools
	}
	carrier, err := r.seal(state.message, translated)
	if err != nil {
		return nil, err
	}
	state.carrier = carrier
	var output [][]byte
	for _, frame := range state.frames {
		frames, err := state.translate(ctx, destination, model, original, translated, frame)
		if err != nil {
			state.failure = err
			return nil, err
		}
		output = append(output, frames...)
	}
	state.frames = nil
	state.finished = true
	return output, nil
}

func (s *CopilotChatStream) translate(ctx context.Context, destination, model string, original, translated, frame []byte) ([][]byte, error) {
	_, data, done, err := parseSSEFrame(frame)
	if err != nil {
		return nil, err
	}
	if !done && len(data) > 0 {
		root, err := decodeObject(data)
		if err != nil {
			return nil, err
		}
		choices, _ := root["choices"].([]any)
		for _, raw := range choices {
			delta := objectValue(objectValue(raw)["delta"])
			delete(delta, "reasoning_text")
			if s.carrier != "" && !s.reasoningSent {
				if text, exists := s.message["reasoning_text"]; exists {
					delta["reasoning_content"] = text
				}
				s.reasoningSent = true
			}
		}
		data, err = json.Marshal(root)
		if err != nil {
			return nil, err
		}
		frame = append(append([]byte("data: "), data...), []byte("\n\n")...)
	}
	frames := registry.TranslateStream(ctx, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, model, original, translated, frame, &s.responses)
	var output [][]byte
	for _, frame := range frames {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			return nil, err
		}
		payload, err := decodeObject(data)
		if err != nil {
			return nil, err
		}
		event := stringValue(payload["type"])
		item := objectValue(payload["item"])
		if s.carrier != "" && stringValue(item["type"]) == "reasoning" {
			item["encrypted_content"] = s.carrier
			s.signed = true
		}
		if event == "response.completed" {
			root := objectValue(payload["response"])
			applyChatFinish(root, s.finishReason)
			if stringValue(root["status"]) == "incomplete" {
				payload["type"] = "response.incomplete"
			}
		}
		if s.carrier != "" && event == "response.completed" {
			response, err := json.Marshal(payload["response"])
			if err != nil {
				return nil, err
			}
			response, err = attachChatCarrier(response, s.carrier, s.finishReason)
			if err != nil {
				return nil, err
			}
			root, err := decodeObject(response)
			if err != nil {
				return nil, err
			}
			payload["response"] = root
			if !s.signed {
				items, _ := root["output"].([]any)
				item := objectValue(items[len(items)-1])
				for _, kind := range []string{"response.output_item.added", "response.output_item.done"} {
					frames, err := s.emit(destination, model, map[string]any{"type": kind, "item": item, "output_index": len(items) - 1})
					if err != nil {
						return nil, err
					}
					output = append(output, frames...)
				}
			}
		}
		frames, err := s.emit(destination, model, payload)
		if err != nil {
			return nil, err
		}
		output = append(output, frames...)
	}
	return output, nil
}

func (s *CopilotChatStream) emit(destination, model string, payload map[string]any) ([][]byte, error) {
	payload["sequence_number"] = s.sequence
	s.sequence++
	frame := claudeSSE(stringValue(payload["type"]), payload)
	if destination == "claude" {
		frames, err := responsesStreamToClaude(model, frame, &s.claude)
		if err != nil {
			return nil, err
		}
		return s.orderedClaudeBlocks(frames)
	}
	return [][]byte{frame}, nil
}

func (s *CopilotChatStream) orderedClaudeBlocks(frames [][]byte) ([][]byte, error) {
	if s.blockFrames == nil {
		s.blockFrames = make(map[int][][]byte)
		s.blockDone = make(map[int]bool)
	}
	var output [][]byte
	for _, frame := range frames {
		_, data, _, err := parseSSEFrame(frame)
		if err != nil {
			return nil, err
		}
		payload, err := decodeObject(data)
		if err != nil {
			return nil, err
		}
		kind := stringValue(payload["type"])
		index := int(int64Value(payload["index"]))
		if kind == "content_block_start" {
			if _, exists := s.blockFrames[index]; exists {
				return nil, ErrCopilotReasoning
			}
			s.blockFrames[index] = nil
			s.blockOrder = append(s.blockOrder, index)
		}
		if kind == "content_block_start" || kind == "content_block_delta" || kind == "content_block_stop" {
			if _, exists := s.blockFrames[index]; !exists {
				return nil, ErrCopilotReasoning
			}
			s.blockFrames[index] = append(s.blockFrames[index], frame)
			if kind == "content_block_stop" {
				s.blockDone[index] = true
			}
			for len(s.blockOrder) > 0 {
				first := s.blockOrder[0]
				output = append(output, s.blockFrames[first]...)
				s.blockFrames[first] = nil
				if !s.blockDone[first] {
					break
				}
				delete(s.blockFrames, first)
				delete(s.blockDone, first)
				s.blockOrder = s.blockOrder[1:]
			}
		} else {
			if len(s.blockOrder) > 0 && (kind == "message_delta" || kind == "message_stop") {
				return nil, ErrCopilotReasoning
			}
			output = append(output, frame)
		}
	}
	return output, nil
}
