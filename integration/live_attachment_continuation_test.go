//go:build !windows

package integration

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
)

func TestAttachmentNativeResponsesHistoryPreservesOriginalAndPublicCalls(t *testing.T) {
	call := liveAttachmentCapturedToolCall{ItemID: "fc_owned", CallID: "call-owned", Name: "exec_command", Arguments: map[string]any{"cmd": "printf CPA_CONTINUATION_OK"}}
	calls := map[string]liveAttachmentCapturedToolCall{call.CallID: call}
	for _, fixture := range []struct {
		name, id, arguments string
		omit, valid         bool
	}{
		{"exact", call.ItemID, `{"cmd":"printf CPA_CONTINUATION_OK"}`, false, true},
		{"wrong-item", "fc_foreign", `{"cmd":"printf CPA_CONTINUATION_OK"}`, false, false},
		{"wrong-arguments", call.ItemID, `{"cmd":"printf OTHER"}`, false, false},
		{"missing-assistant", call.ItemID, `{"cmd":"printf CPA_CONTINUATION_OK"}`, true, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			input := []any{}
			if !fixture.omit {
				input = append(input, map[string]any{"type": "function_call", "id": fixture.id, "call_id": call.CallID, "name": call.Name, "arguments": fixture.arguments})
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": call.CallID, "output": "Process exited with code 0\nOutput:\nCPA_CONTINUATION_OK"})
			body, err := json.Marshal(map[string]any{"input": input})
			if err != nil {
				t.Fatal(err)
			}
			if liveAttachmentResponsesHistoryCorrelated(string(body), calls, calls) != fixture.valid {
				t.Fatal("native Responses history identity/arguments verdict was incorrect")
			}
		})
	}
}

func TestAttachmentPNGChatCarrierPreservesOriginalToolIdentity(t *testing.T) {
	carrier := "cpa_tool_v1_" + base64.RawURLEncoding.EncodeToString([]byte(`{"i":"fc_call-png","c":"call-png"}`))
	call := liveAttachmentCapturedToolCall{ItemID: "fc_call-png", CallID: "call-png", Name: "exec_command", Arguments: map[string]any{"cmd": "printf CPA_CONTINUATION_OK"}}
	original := map[string]liveAttachmentCapturedToolCall{call.CallID: call}
	public := map[string]liveAttachmentCapturedToolCall{call.CallID: call}
	request := liveAttachmentChatRequestEvidence{
		ToolCalls:   []liveChatAttachmentToolCall{{ID: carrier, Name: call.Name, Arguments: call.Arguments}},
		ToolResults: []liveChatAttachmentToolResult{{ID: carrier, Content: "Process exited with code 0\nOutput:\nCPA_CONTINUATION_OK"}},
	}
	if names, valid := liveAttachmentChatHistoryCorrelations(request, original, public, "exec_command"); !valid || names[carrier] != call.Name {
		t.Fatal("single exec continuation lost original Responses item/call identity")
	}
	for _, alter := range []func(*liveAttachmentChatRequestEvidence){
		func(value *liveAttachmentChatRequestEvidence) { value.ToolCalls[0].ID = "call-png" },
		func(value *liveAttachmentChatRequestEvidence) {
			value.ToolCalls[0].Arguments = map[string]any{"cmd": "printf OTHER"}
		},
		func(value *liveAttachmentChatRequestEvidence) { value.ToolResults[0].ID = "other" },
		func(value *liveAttachmentChatRequestEvidence) { value.ToolCalls[0].Name = "view_image" },
	} {
		changed := request
		changed.ToolCalls = append([]liveChatAttachmentToolCall(nil), request.ToolCalls...)
		changed.ToolResults = append([]liveChatAttachmentToolResult(nil), request.ToolResults...)
		alter(&changed)
		if _, valid := liveAttachmentChatHistoryCorrelations(changed, original, public, "exec_command"); valid {
			t.Fatal("uncorrelated PNG continuation carrier was accepted")
		}
	}
}

func TestAttachmentPNGToolOutputRequiresSuccessfulExactMarker(t *testing.T) {
	for _, fixture := range []struct {
		output string
		valid  bool
	}{
		{"Chunk ID: fixture\nWall time: 1 seconds\nProcess exited with code 0\nOutput:\nCPA_CONTINUATION_OK", true},
		{"Process exited with code 0\nFinal output:\nCPA_CONTINUATION_OK\n", true},
		{"CPA_CONTINUATION_OK", false},
		{"Process exited with code 1\nOutput:\nCPA_CONTINUATION_OK", false},
		{"Process exited with code 0\nOutput:\nCPA_CONTINUATION_OK\nProcess exited with code 1", false},
		{"Process exited with code 0\nOutput:\nOTHER", false},
	} {
		if liveAttachmentPNGCommandOutput(fixture.output) != fixture.valid {
			t.Fatalf("unexpected continuation output verdict for %q", fixture.output)
		}
	}
}

func TestAttachmentCodexPNGRequiresSuccessfulContinuationAndMarker(t *testing.T) {
	for _, candidate := range liveAttachmentCases() {
		if candidate.client != "codex" || candidate.media != "png" {
			continue
		}
		t.Run(candidate.cell, func(t *testing.T) {
			final := "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"CPA_CONTINUATION_OK. The top is red and the bottom is blue.\"}}\n{\"type\":\"turn.completed\"}\n"
			for _, fixture := range []struct {
				name, command, output string
				exit                  int
				valid                 bool
			}{
				{"success", "printf CPA_CONTINUATION_OK", "CPA_CONTINUATION_OK", 0, true},
				{"wrong-command", "printf OTHER", "CPA_CONTINUATION_OK", 0, false},
				{"missing-output", "printf CPA_CONTINUATION_OK", "", 0, false},
				{"failed", "printf CPA_CONTINUATION_OK", "CPA_CONTINUATION_OK", 1, false},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					command := fmt.Sprintf("{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\",\"status\":\"completed\",\"command\":%q,\"aggregated_output\":%q,\"exit_code\":%d}}\n", fixture.command, fixture.output, fixture.exit)
					evidence := inspectLiveAttachmentClient(candidate, []byte(command+final))
					if !evidence.ClientFinal || !evidence.ClientAnswer || evidence.ClientToolCall != fixture.valid || evidence.ClientToolResult != fixture.valid {
						t.Fatal("PNG answer bypassed or lost the successful continuation proof")
					}
				})
			}
			if evidence := inspectLiveAttachmentClient(candidate, []byte(final)); evidence.ClientToolCall || evidence.ClientToolResult {
				t.Fatal("final text alone established a tool roundtrip")
			}
			withoutMarker := "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"The top is red and the bottom is blue.\"}}\n{\"type\":\"turn.completed\"}\n"
			if inspectLiveAttachmentClient(candidate, []byte(withoutMarker)).ClientAnswer {
				t.Fatal("final answer omitted the required continuation marker")
			}
		})
	}
}
