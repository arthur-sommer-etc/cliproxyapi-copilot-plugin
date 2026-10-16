package compact

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

const testScope = "account:42|origin:https://api.example.test|model:gpt-test"

func TestPrepareAdaptsCompactionRequest(t *testing.T) {
	body := []byte(`{"model":"gpt-test","tools":[{"type":"function","name":"danger"}],"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Keep the active goal."}]},{"type":"compaction_trigger"}]}`)
	prepared, requested, err := Prepare(body, KeyMaterial{Scope: testScope, Secret: testSecret}, nil)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if !requested {
		t.Fatal("Prepare() requested = false, want true")
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(prepared, &request); err != nil {
		t.Fatalf("decode prepared request: %v", err)
	}
	var input []map[string]json.RawMessage
	if err := json.Unmarshal(request["input"], &input); err != nil {
		t.Fatalf("decode prepared input: %v", err)
	}
	if len(input) != 2 {
		t.Fatalf("prepared input item count = %d, want original history and summary instruction", len(input))
	}
	var firstType string
	if err := json.Unmarshal(input[0]["type"], &firstType); err != nil || firstType != "message" {
		t.Fatalf("first input item type = %q, error = %v", firstType, err)
	}
	var finalRole string
	if err := json.Unmarshal(input[1]["role"], &finalRole); err != nil || finalRole != "developer" {
		t.Fatalf("final input role = %q, error = %v", finalRole, err)
	}
	if bytes.Contains(request["input"], []byte("compaction_trigger")) {
		t.Fatal("prepared input still contains compaction_trigger")
	}
	if _, exists := request["tools"]; exists {
		t.Fatal("summary request retained tools")
	}
	if string(request["tool_choice"]) != `"none"` {
		t.Fatalf("tool_choice = %s, want none", request["tool_choice"])
	}
}

func TestCompleteCapsuleReplaysOnLaterRequest(t *testing.T) {
	response := []byte(`{"id":"resp_123","object":"response","status":"completed","model":"gpt-test","output":[{"id":"msg_123","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"The active goal is to finish the bridge. The next step is integration."}]}],"usage":{"input_tokens":41,"output_tokens":18}}`)
	completed, err := Complete(response, KeyMaterial{Scope: testScope, Secret: testSecret}, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(response, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(completed, &after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before["id"], after["id"]) || !bytes.Equal(before["model"], after["model"]) || !bytes.Equal(before["usage"], after["usage"]) {
		t.Fatalf("response metadata changed: before=%s after=%s", response, completed)
	}
	var output []map[string]json.RawMessage
	if err := json.Unmarshal(after["output"], &output); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(output) != 2 {
		t.Fatalf("output item count = %d, want assistant summary and compaction item", len(output))
	}
	var kind, capsule string
	if err := json.Unmarshal(output[1]["type"], &kind); err != nil || kind != "compaction" {
		t.Fatalf("compaction type = %q, error = %v", kind, err)
	}
	if err := json.Unmarshal(output[1]["encrypted_content"], &capsule); err != nil || !strings.HasPrefix(capsule, capsulePrefix) {
		t.Fatalf("capsule prefix invalid: %q, error = %v", capsule, err)
	}
	if strings.Contains(capsule, "The active goal is to finish the bridge") {
		t.Fatal("capsule exposed the plaintext summary")
	}
	request := []byte(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]},{"type":"compaction","encrypted_content":"` + capsule + `"}]}`)
	prepared, requested, err := Prepare(request, KeyMaterial{Scope: testScope, Secret: testSecret}, nil)
	if err != nil {
		t.Fatalf("replay Prepare() error = %v", err)
	}
	if requested {
		t.Fatal("replay without a trigger requested compaction")
	}
	if bytes.Contains(prepared, []byte(capsule)) || bytes.Contains(prepared, []byte(capsuleNamespace)) {
		t.Fatal("replayed request retained the opaque capsule")
	}
	if !bytes.Contains(prepared, []byte("The active goal is to finish the bridge")) {
		t.Fatal("replayed request omitted the decrypted summary")
	}
	idempotent, err := Complete(completed, KeyMaterial{Scope: testScope, Secret: testSecret}, nil)
	if err != nil {
		t.Fatalf("idempotent Complete() error = %v", err)
	}
	if !bytes.Equal(idempotent, completed) {
		t.Fatal("Complete() changed a response that already had its plugin capsule")
	}
}

func TestNativeCompactionPassesThrough(t *testing.T) {
	body := []byte(` { "model":"gpt-test", "input":[{"type":"compaction","encrypted_content":"native-opaque-content"}] } `)
	prepared, requested, err := Prepare(body, KeyMaterial{}, nil)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if requested || !bytes.Equal(prepared, body) {
		t.Fatalf("native request changed: requested=%v prepared=%s", requested, prepared)
	}
	response := []byte(` { "status":"completed", "output":[{"type":"compaction","encrypted_content":"native-opaque-content"}], "usage":{"total_tokens":3} } `)
	completed, err := Complete(response, KeyMaterial{}, nil)
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if !bytes.Equal(completed, response) {
		t.Fatalf("native response changed: %s", completed)
	}
}

func TestCompleteRejectsDuplicateNativeCompactionItems(t *testing.T) {
	response := []byte(`{"status":"completed","output":[{"type":"compaction","encrypted_content":"native-first"},{"type":"compaction","encrypted_content":"native-second"}]}`)
	_, err := Complete(response, KeyMaterial{}, nil)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("Complete() error = %v, want ErrInvalidResponse", err)
	}
}

func TestPrepareRejectsUntrustedCapsules(t *testing.T) {
	completed, err := Complete(summaryResponse("keep this summary"), KeyMaterial{Scope: testScope, Secret: testSecret}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(completed, &result); err != nil {
		t.Fatal(err)
	}
	var output []map[string]json.RawMessage
	if err := json.Unmarshal(result["output"], &output); err != nil {
		t.Fatal(err)
	}
	var capsule string
	if err := json.Unmarshal(output[1]["encrypted_content"], &capsule); err != nil {
		t.Fatal(err)
	}
	mutated := []byte(capsule)
	encodedIndex := len(capsulePrefix)
	if mutated[encodedIndex] == 'A' {
		mutated[encodedIndex] = 'B'
	} else {
		mutated[encodedIndex] = 'A'
	}
	tests := []struct {
		name    string
		capsule string
		scope   string
		secret  []byte
		want    error
	}{
		{name: "tampered", capsule: string(mutated), scope: testScope, secret: testSecret, want: ErrInvalidCapsule},
		{name: "wrong scope", capsule: capsule, scope: "different-account|model:gpt-test", secret: testSecret, want: ErrInvalidCapsule},
		{name: "wrong key", capsule: capsule, scope: testScope, secret: []byte("abcdef0123456789abcdef0123456789"), want: ErrInvalidCapsule},
		{name: "unsupported version", capsule: capsuleNamespace + "v2:opaque", scope: testScope, secret: testSecret, want: ErrUnsupportedCapsule},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"input":[{"type":"compaction","encrypted_content":"` + test.capsule + `"}]}`)
			_, _, err := Prepare(body, KeyMaterial{Scope: test.scope, Secret: test.secret}, nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("Prepare() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPrepareRejectsDuplicateTriggers(t *testing.T) {
	body := []byte(`{"input":[{"type":"compaction_trigger"},{"type":"compaction_trigger"}]}`)
	_, _, err := Prepare(body, KeyMaterial{Scope: testScope, Secret: testSecret}, nil)
	if !errors.Is(err, ErrDuplicateTrigger) {
		t.Fatalf("Prepare() error = %v, want ErrDuplicateTrigger", err)
	}
}

func TestCompleteRequiresSuccessfulPlainSummary(t *testing.T) {
	tests := []struct {
		name     string
		response []byte
		want     error
	}{
		{name: "incomplete", response: []byte(`{"status":"incomplete","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"not accepted"}]}]}`), want: ErrIncompleteResponse},
		{name: "failed", response: []byte(`{"status":"failed","output":[]}`), want: ErrIncompleteResponse},
		{name: "empty text", response: summaryResponse(" \n "), want: ErrSummaryMissing},
		{name: "incomplete message", response: []byte(`{"status":"completed","output":[{"type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"not accepted"}]}]}`), want: ErrSummaryMissing},
		{name: "reasoning only", response: []byte(`{"status":"completed","output":[{"type":"reasoning","summary":[{"text":"not plain output"}]}]}`), want: ErrSummaryMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Complete(test.response, KeyMaterial{Scope: testScope, Secret: testSecret}, nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("Complete() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPrepareRequiresKeyOnlyWhenCompactionIsPresent(t *testing.T) {
	ordinary := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	prepared, requested, err := Prepare(ordinary, KeyMaterial{}, nil)
	if err != nil || requested || !bytes.Equal(prepared, ordinary) {
		t.Fatalf("ordinary Prepare() = %s, %v, %v", prepared, requested, err)
	}
	trigger := []byte(`{"input":[{"type":"compaction_trigger"}]}`)
	_, _, err = Prepare(trigger, KeyMaterial{}, nil)
	if !errors.Is(err, ErrScopeRequired) {
		t.Fatalf("trigger Prepare() error = %v, want ErrScopeRequired", err)
	}
	longSecret := append(append([]byte(nil), testSecret...), 'x')
	_, _, err = Prepare(trigger, KeyMaterial{Scope: testScope, Secret: longSecret}, nil)
	if !errors.Is(err, ErrScopeRequired) {
		t.Fatalf("trigger Prepare() with a non-32-byte key error = %v, want ErrScopeRequired", err)
	}
}

func summaryResponse(summary string) []byte {
	response, err := json.Marshal(map[string]any{
		"status": "completed",
		"output": []any{map[string]any{
			"type":    "message",
			"role":    "assistant",
			"status":  "completed",
			"content": []any{map[string]string{"type": "output_text", "text": summary}},
		}},
	})
	if err != nil {
		panic(err)
	}
	return response
}

func TestPrepareMigratesLegacyCapsuleAndCompletesWithActiveKey(t *testing.T) {
	legacy := KeyMaterial{Scope: "legacy-scope", Secret: bytes.Repeat([]byte{0x77}, 32)}
	active := KeyMaterial{Scope: "stable-root-scope", Secret: bytes.Repeat([]byte{0x88}, 32)}
	response := summaryResponse("Keep the capsule across token renewal.")
	legacyResult, err := Complete(response, legacy, nil)
	if err != nil {
		t.Fatalf("complete legacy result: %v", err)
	}
	capsule := compactionCapsuleFromTestResponse(legacyResult)
	if capsule == "" {
		t.Fatalf("legacy result has no capsule: %s", legacyResult)
	}
	request := []byte(`{"input":[{"type":"compaction","encrypted_content":"` + capsule + `"},{"type":"compaction_trigger"}]}`)
	prepared, requested, err := Prepare(request, active, []KeyMaterial{legacy})
	if err != nil || !requested {
		t.Fatalf("prepare migrated request: requested=%v error=%v", requested, err)
	}
	if !bytes.Contains(prepared, []byte("Keep the capsule across token renewal.")) || bytes.Contains(prepared, []byte(capsule)) {
		t.Fatalf("legacy capsule was not expanded: %s", prepared)
	}
	activeResult, err := Complete(response, active, []KeyMaterial{legacy})
	if err != nil {
		t.Fatalf("complete active result: %v", err)
	}
	activeCapsule := compactionCapsuleFromTestResponse(activeResult)
	activeReplay := []byte(`{"input":[{"type":"compaction","encrypted_content":"` + activeCapsule + `"}]}`)
	if _, _, err := Prepare(activeReplay, active, nil); err != nil {
		t.Fatalf("active capsule did not use stable key: %v", err)
	}
}

func compactionCapsuleFromTestResponse(payload []byte) string {
	var result map[string]any
	if json.Unmarshal(payload, &result) != nil {
		return ""
	}
	output, ok := result["output"].([]any)
	if !ok || len(output) < 2 {
		return ""
	}
	item, ok := output[1].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := item["encrypted_content"].(string)
	return value
}
