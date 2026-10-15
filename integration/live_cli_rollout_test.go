//go:build !windows

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type liveCodexRollout struct {
	body []byte
	meta map[string]any
	rows []liveCodexRolloutRow
}

type liveCodexRolloutRow struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func inspectLiveCodexDelegation(t *testing.T, root, parentID string) bool {
	t.Helper()
	rollouts := map[string]liveCodexRollout{}
	err := filepath.WalkDir(filepath.Join(root, "sessions"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 8<<20 {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rollout := parseLiveCodexRollout(body)
		if id, ok := rollout.meta["id"].(string); ok {
			rollouts[id] = rollout
		}
		return nil
	})
	if err != nil {
		t.Log("Codex rollout evidence unavailable")
		return false
	}
	parent, ok := rollouts[parentID]
	if !ok {
		return false
	}
	children := map[string]liveCodexRollout{}
	for id, rollout := range rollouts {
		if rolloutParentID(rollout.meta) == parentID {
			children[id] = rollout
		}
	}
	hasSpawn := false
	for _, row := range parent.rows {
		if row.Type == "response_item" && row.Payload["type"] == "function_call" && row.Payload["name"] == "spawn_agent" && row.Payload["namespace"] == "collaboration" {
			hasSpawn = true
		}
	}
	if hasSpawn {
		if directory := liveDebugArtifactDirectory(t, "codex-rollout"); directory != "" {
			if err := os.WriteFile(filepath.Join(directory, "parent.jsonl"), parent.body, 0600); err != nil {
				t.Fatal("could not retain private parent rollout")
			}
			childIDs := make([]string, 0, len(children))
			for id := range children {
				childIDs = append(childIDs, id)
			}
			slices.Sort(childIDs)
			for index, id := range childIDs {
				if err := os.WriteFile(filepath.Join(directory, "child-"+strconv.Itoa(index+1)+".jsonl"), children[id].body, 0600); err != nil {
					t.Fatal("could not retain private child rollout")
				}
			}
			t.Logf("retained parent and %d linked child rollouts: %s", len(children), directory)
		}
	}
	return verifyLiveCodexDelegation(parentID, parent, children)
}

func parseLiveCodexRollout(body []byte) liveCodexRollout {
	rollout := liveCodexRollout{body: body}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		var row liveCodexRolloutRow
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			return liveCodexRollout{body: body}
		}
		if row.Type == "session_meta" && rollout.meta == nil {
			rollout.meta = row.Payload
		}
		rollout.rows = append(rollout.rows, row)
	}
	if scanner.Err() != nil {
		return liveCodexRollout{body: body}
	}
	return rollout
}

func rolloutParentID(meta map[string]any) string {
	if id, ok := meta["parent_thread_id"].(string); ok && id != "" {
		return id
	}
	source, _ := meta["source"].(map[string]any)
	for _, subagent := range source {
		object, _ := subagent.(map[string]any)
		spawn, _ := object["thread_spawn"].(map[string]any)
		if id, ok := spawn["parent_thread_id"].(string); ok {
			return id
		}
	}
	return ""
}

func verifyLiveCodexDelegation(parentID string, parent liveCodexRollout, children map[string]liveCodexRollout) bool {
	if parent.meta["id"] != parentID {
		return false
	}
	parentPath, _ := parent.meta["agent_path"].(string)
	if parentPath == "" {
		parentPath = "/root"
	}
	for callIndex, row := range parent.rows {
		call := row.Payload
		if row.Type != "response_item" || call["type"] != "function_call" || call["namespace"] != "collaboration" || call["name"] != "spawn_agent" {
			continue
		}
		arguments := rolloutJSONField(call["arguments"])
		taskName, _ := arguments["task_name"].(string)
		if taskName == "" {
			continue
		}
		spawnIndex, spawn := rolloutFunctionResult(parent.rows, callIndex, call["call_id"])
		childPath, _ := spawn["task_name"].(string)
		if spawnIndex <= callIndex || childPath != parentPath+"/"+taskName {
			continue
		}
		for childID, child := range children {
			if childID == "" || child.meta["id"] != childID || rolloutParentID(child.meta) != parentID || child.meta["agent_path"] != childPath || !rolloutChildCompleted(child) {
				continue
			}
			for waitIndex := spawnIndex + 1; waitIndex < len(parent.rows); waitIndex++ {
				wait := parent.rows[waitIndex]
				if wait.Type != "response_item" || wait.Payload["type"] != "function_call" || wait.Payload["namespace"] != "collaboration" || wait.Payload["name"] != "wait_agent" {
					continue
				}
				resultIndex, result := rolloutFunctionResult(parent.rows, waitIndex, wait.Payload["call_id"])
				if resultIndex > waitIndex && result["timed_out"] == false && result["message"] == "Wait completed." && rolloutChildHandback(parent.rows, spawnIndex, childPath, parentPath) {
					return true
				}
			}
		}
	}
	return false
}

func rolloutJSONField(raw any) map[string]any {
	text, _ := raw.(string)
	var value map[string]any
	if json.Unmarshal([]byte(text), &value) != nil {
		return nil
	}
	return value
}

func rolloutFunctionResult(rows []liveCodexRolloutRow, after int, callID any) (int, map[string]any) {
	expectedID, ok := callID.(string)
	if !ok || expectedID == "" {
		return -1, nil
	}
	for index := after + 1; index < len(rows); index++ {
		row := rows[index]
		if row.Type == "response_item" && row.Payload["type"] == "function_call_output" && row.Payload["call_id"] == expectedID {
			return index, rolloutJSONField(row.Payload["output"])
		}
	}
	return -1, nil
}

func rolloutChildCompleted(child liveCodexRollout) bool {
	assistantAnswer := false
	turnID := ""
	startIndex := -1
	for index, row := range child.rows {
		if row.Type == "event_msg" && (row.Payload["type"] == "task_started" || row.Payload["type"] == "turn_started") {
			turnID, _ = row.Payload["turn_id"].(string)
			startIndex = index
		}
	}
	if startIndex == -1 || turnID == "" {
		return false
	}
	for _, row := range child.rows[startIndex+1:] {
		if row.Type == "response_item" && row.Payload["type"] == "message" && row.Payload["role"] == "assistant" {
			content, _ := row.Payload["content"].([]any)
			for _, raw := range content {
				block, _ := raw.(map[string]any)
				text, _ := block["text"].(string)
				assistantAnswer = assistantAnswer || block["type"] == "output_text" && rolloutHasAnswer(text)
			}
		}
		if row.Type == "event_msg" && (row.Payload["type"] == "task_complete" || row.Payload["type"] == "turn_complete") {
			text, _ := row.Payload["last_agent_message"].(string)
			if turnID != "" && row.Payload["turn_id"] == turnID && assistantAnswer && row.Payload["error"] == nil && rolloutHasAnswer(text) {
				return true
			}
		}
	}
	return false
}

func rolloutChildHandback(rows []liveCodexRolloutRow, after int, childPath, parentPath string) bool {
	for _, row := range rows[after+1:] {
		if row.Type == "inter_agent_communication" && row.Payload["author"] == childPath && row.Payload["recipient"] == parentPath {
			text, _ := row.Payload["content"].(string)
			if rolloutHasAnswer(text) {
				return true
			}
		}
		if row.Type == "response_item" && row.Payload["type"] == "agent_message" && row.Payload["author"] == childPath && row.Payload["recipient"] == parentPath {
			content, _ := row.Payload["content"].([]any)
			for _, raw := range content {
				block, _ := raw.(map[string]any)
				text, _ := block["text"].(string)
				if block["type"] == "input_text" && rolloutHasAnswer(text) {
					return true
				}
			}
		}
	}
	return false
}

func rolloutHasAnswer(text string) bool {
	return regexp.MustCompile(`\b323\b`).MatchString(text)
}

func TestLiveCodexRecordedV2Delegation(t *testing.T) {
	parentBody, err := os.ReadFile(filepath.Join("testdata", "codex_v2_parent_completed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	childBody, err := os.ReadFile(filepath.Join("testdata", "codex_v2_child_completed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	parent := parseLiveCodexRollout(parentBody)
	child := parseLiveCodexRollout(childBody)
	children := map[string]liveCodexRollout{"fixture-child": child}
	if !verifyLiveCodexDelegation("fixture-parent", parent, children) {
		t.Fatal("recorded parent spawn, linked completed child, wait and handback were not verified")
	}
	for _, test := range []struct {
		name   string
		mutate func(*liveCodexRollout, *liveCodexRollout)
	}{
		{name: "different current parent", mutate: func(parent, _ *liveCodexRollout) { parent.meta["id"] = "other-parent" }},
		{name: "unlinked child", mutate: func(_, child *liveCodexRollout) { child.meta["parent_thread_id"] = "other-parent" }},
		{name: "different child identity", mutate: func(_, child *liveCodexRollout) { child.meta["id"] = "other-child" }},
		{name: "different child task", mutate: func(_, child *liveCodexRollout) { child.meta["agent_path"] = "/root/other" }},
		{name: "missing spawn", mutate: func(parent, _ *liveCodexRollout) {
			parent.rows = slices.DeleteFunc(parent.rows, func(row liveCodexRolloutRow) bool { return row.Payload["name"] == "spawn_agent" })
		}},
		{name: "missing successful spawn output", mutate: func(parent, _ *liveCodexRollout) {
			for _, row := range parent.rows {
				if row.Payload["type"] == "function_call_output" && strings.Contains(stringField(row.Payload["output"]), "task_name") {
					row.Payload["output"] = "spawn failed"
				}
			}
		}},
		{name: "child incomplete", mutate: func(_, child *liveCodexRollout) {
			child.rows = slices.DeleteFunc(child.rows, func(row liveCodexRolloutRow) bool {
				return row.Type == "event_msg" && row.Payload["type"] == "task_complete"
			})
		}},
		{name: "child has only an earlier completed turn", mutate: func(_, child *liveCodexRollout) {
			child.rows = append(child.rows, liveCodexRolloutRow{Type: "event_msg", Payload: map[string]any{"type": "task_started", "turn_id": "new-incomplete-turn"}})
		}},
		{name: "child failed", mutate: func(_, child *liveCodexRollout) {
			for _, row := range child.rows {
				if row.Payload["type"] == "task_complete" {
					row.Payload["error"] = map[string]any{"message": "failed"}
				}
			}
		}},
		{name: "child has no actual assistant answer", mutate: func(_, child *liveCodexRollout) {
			child.rows = slices.DeleteFunc(child.rows, func(row liveCodexRolloutRow) bool {
				return row.Payload["type"] == "message" && row.Payload["role"] == "assistant"
			})
		}},
		{name: "missing wait", mutate: func(parent, _ *liveCodexRollout) {
			parent.rows = slices.DeleteFunc(parent.rows, func(row liveCodexRolloutRow) bool { return row.Payload["name"] == "wait_agent" })
		}},
		{name: "wait timed out", mutate: func(parent, _ *liveCodexRollout) {
			for _, row := range parent.rows {
				if row.Payload["type"] == "function_call_output" && strings.Contains(stringField(row.Payload["output"]), "timed_out") {
					row.Payload["output"] = `{"message":"Wait timed out.","timed_out":true}`
				}
			}
		}},
		{name: "missing child handback", mutate: func(parent, _ *liveCodexRollout) {
			parent.rows = slices.DeleteFunc(parent.rows, func(row liveCodexRolloutRow) bool {
				return row.Type == "inter_agent_communication" || row.Payload["type"] == "agent_message"
			})
		}},
		{name: "parent claims answer without child", mutate: func(parent, child *liveCodexRollout) {
			parent.rows = slices.DeleteFunc(parent.rows, func(row liveCodexRolloutRow) bool {
				return row.Payload["type"] == "function_call" || row.Payload["type"] == "function_call_output"
			})
			child.rows = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := parseLiveCodexRollout(parentBody)
			child := parseLiveCodexRollout(childBody)
			test.mutate(&parent, &child)
			if verifyLiveCodexDelegation("fixture-parent", parent, map[string]liveCodexRollout{"fixture-child": child}) {
				t.Fatal("incomplete or unrelated rollout chain was counted as completed delegation")
			}
		})
	}
}

func stringField(raw any) string {
	text, _ := raw.(string)
	return text
}
