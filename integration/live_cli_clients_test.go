//go:build !windows

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/arthur-sommer-etc/cliproxyapi-copilot-plugin/internal/redact"
)

type liveCLIResult struct {
	exitCode             int
	timedOut             bool
	final                bool
	finalText            bool
	turnCompleted        bool
	sourceURL            bool
	errorCode            string
	truncated            bool
	toolStarted          map[string]int
	toolCompleted        map[string]int
	toolFailed           map[string]int
	toolIDs              map[string]string
	startedIDs           map[string]string
	completedIDs         map[string]string
	spawnedAgents        map[string]bool
	doneAgents           map[string]bool
	eventTypes           map[string]int
	threadID             string
	verifiedV2Delegation bool
	finalOutput          string
	delegatedResultText  map[string][]string
}

type liveCLIBuffer struct {
	mu        sync.Mutex
	bytes     []byte
	truncated bool
}

func (b *liveCLIBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	const maxBytes = 4 << 20
	available := maxBytes - len(b.bytes)
	if available > 0 {
		b.bytes = append(b.bytes, p[:min(len(p), available)]...)
	}
	if len(p) > available {
		b.truncated = true
	}
	return len(p), nil
}

func TestLiveCLIClients(t *testing.T) {
	if os.Getenv("CPA_LIVE_COPILOT_CLIENTS") != "1" {
		t.Skip("set CPA_LIVE_COPILOT_CLIENTS=1 to run real CLI clients")
	}
	binary := os.Getenv("CPA_BINARY")
	if binary == "" {
		t.Fatal("CPA_BINARY is required for live CLI clients")
	}
	profile := liveProfileFromEnvironment(t)
	base, stop := startLiveNativeHost(t, binary, profile.StorageJSON, profile.AuthMode, liveEndpointOverrides(profile.Catalog))
	defer stop()
	t.Logf("live client profile source=%s auth_mode=%s model_count=%d", profile.Source, profile.AuthMode, len(profile.Catalog))
	for _, client := range []string{"claude", "codex"} {
		client := client
		t.Run(client, func(t *testing.T) {
			testLiveCLIClient(t, client, base)
		})
	}
}

func TestLiveCapturedCLIClients(t *testing.T) {
	if os.Getenv("CPA_LIVE_CAPTURED_CLIENTS") != "1" {
		t.Skip("set CPA_LIVE_CAPTURED_CLIENTS=1 to run captured real CLI clients")
	}
	phase, err := liveCapturedCLIAttemptPhase(os.Getenv("CPA_LIVE_CAPTURED_CLIENTS_ATTEMPT"))
	if err != nil {
		t.Fatal(err)
	}
	caseValue, casesSet := os.LookupEnv("CPA_LIVE_CAPTURED_CLIENTS_CASES")
	clients, err := liveCapturedCLISelectedClients(caseValue, casesSet)
	if err != nil {
		t.Fatal(err)
	}
	claimCells := liveOriginalCLIClaimCells(phase)
	if len(claimCells) != 6 {
		t.Fatal("captured CLI attempt did not resolve to the fixed six claim cells")
	}
	for _, key := range claimCells {
		if !liveOriginalCLIClaimCell(key) {
			t.Fatalf("captured CLI attempt contains an invalid claim cell %q", key)
		}
	}
	gate, base, stop := startLiveCapturedHarness(t, nil, liveEndpointOverrides(nil))
	defer stop()
	models := make([]string, 0, len(clients))
	for _, client := range clients {
		model := "claude-haiku-5.5"
		if client == "codex" {
			model = "gpt-6-luna"
		}
		models = append(models, model)
	}
	for _, model := range models {
		if !liveCapturedCatalogAdvertisesEndpoint(gate, model, liveCopilotRoutes[model]) {
			t.Fatalf("captured catalog did not advertise the original endpoint for %s", model)
		}
	}
	for _, client := range clients {
		client := client
		t.Run(client, func(t *testing.T) {
			testLiveCLIClientWithGate(t, client, base, gate, phase)
		})
	}
	if _, inferenceCount, _, _, err := gate.countsFor(""); err != nil || inferenceCount == 0 || livePacketDeniedCount(gate) != 0 {
		t.Fatal("captured original-client dispatch or request/response recording failed")
	}
}

func testLiveCLIClient(t *testing.T, client, base string) {
	testLiveCLIClientWithGate(t, client, base, nil, "L-latest-dependencies")
}

func testLiveCLIClientWithGate(t *testing.T, client, base string, gate *liveServerToolGate, phase string) {
	t.Helper()
	path, err := exec.LookPath(client)
	if err != nil {
		t.Errorf("%s CLI is not installed", client)
		return
	}
	var versionOutput bytes.Buffer
	_, err = runLiveClient(t, liveClientStartupTimeout, path, []string{"--version"}, func(command *exec.Cmd) {
		command.Stdout = &versionOutput
	})
	if err != nil {
		t.Errorf("%s CLI version check failed", client)
		return
	}
	if client == "codex" && phase == "L-native-stream-revalidation" {
		expected := strings.TrimSpace(os.Getenv("CPA_LIVE_CODEX_EXPECTED_VERSION"))
		if expected == "" || strings.TrimSpace(versionOutput.String()) != "codex-cli "+expected {
			t.Fatal("selected original Codex version did not match its declared version")
		}
	}
	t.Logf("client=%s version=%s", client, strings.TrimSpace(versionOutput.String()))
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal("could not restrict temporary client home")
	}
	if client == "codex" {
		catalogPath := filepath.Join(root, "models.json")
		if err := os.WriteFile(catalogPath, []byte(liveCodexModelCatalog), 0600); err != nil {
			t.Fatal("could not write temporary Codex model catalog")
		}
		config := liveCodexConfiguration(base, catalogPath)
		if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(config), 0600); err != nil {
			t.Fatal("could not write temporary Codex config")
		}
	}
	model := "claude-haiku-5.5"
	if client == "codex" {
		model = "gpt-6-luna"
	}
	t.Logf("client=%s model=%s", client, model)
	baselineOK := true
	t.Run("baseline", func(t *testing.T) {
		baseline := runLiveCLIClaimed(t, gate, phase, client, "baseline", func() liveCLIResult {
			return runLiveCLIWithEffort(t, path, client, base, root, "Reply with the single word READY.", "medium")
		})
		logLiveCLIResult(t, client, "baseline", baseline)
		baselineOK = baseline.exitCode == 0 && baseline.final && !baseline.truncated
		if !baselineOK {
			t.Errorf("%s baseline failed", client)
		}
	})
	if !baselineOK {
		t.Log("task=web_search status=NOTRUN reason=baseline_failed")
		t.Log("task=subagent status=NOTRUN reason=baseline_failed")
		return
	}
	searchPrompt := "Invoke WebSearch to find NASA's official Moon facts page. Give one fact and its source URL from the search result. Do not use Bash, curl, or a fact from memory. If the search tool is unavailable, report that failure."
	delegationPrompt := "Invoke Agent with subagent_type general-purpose and a prompt asking it to calculate 17 multiplied by 19. Wait for Agent to return the completed subagent result, then report that answer. If Agent is unavailable, report that failure. Do not calculate the answer yourself."
	if client == "codex" {
		searchPrompt = "Invoke the configured native web_search tool to find NASA's official Moon facts page. Give one fact and its source URL from the search result. Do not use shell commands or a fact from memory. If the native search tool is unavailable, report that failure."
		delegationPrompt = "Invoke collaboration.spawn_agent to delegate this bounded task: calculate 17 multiplied by 19 and return the answer. Then invoke collaboration.wait_agent to wait for that spawned agent to complete before reporting its answer. If either tool is unavailable, report that failure. Do not calculate the answer yourself or invent a subagent result."
	}
	t.Run("web_search", func(t *testing.T) {
		search := runLiveCLIClaimed(t, gate, phase, client, "web-search", func() liveCLIResult {
			return runLiveCLIWithEffort(t, path, client, base, root, searchPrompt, "medium")
		})
		logLiveCLIResult(t, client, "web_search", search)
		if search.exitCode != 0 || !search.final || search.truncated || !liveSearchCompleted(client, search) {
			t.Errorf("%s web search did not complete", client)
		}
	})
	t.Run("subagent", func(t *testing.T) {
		delegation := runLiveCLIClaimed(t, gate, phase, client, "subagent", func() liveCLIResult {
			return runLiveCLIWithEffort(t, path, client, base, root, delegationPrompt, "medium")
		})
		logLiveCLIResult(t, client, "subagent", delegation)
		if delegation.exitCode != 0 || !delegation.final || delegation.truncated || !liveDelegationCompleted(client, delegation) {
			t.Errorf("%s subagent did not complete", client)
		}
	})
}

func runLiveCLIClaimed(t *testing.T, gate *liveServerToolGate, phase, client, task string, run func() liveCLIResult) liveCLIResult {
	t.Helper()
	if gate == nil {
		return run()
	}
	cell := client + "-" + task
	key := phase + "/" + cell
	if !liveOriginalCLIClaimCell(key) {
		t.Fatalf("invalid original CLI claim cell %q", key)
	}
	if err := gate.setPhaseCell(phase, cell); err != nil {
		t.Fatal("original CLI claim cell was not accepted")
	}
	gate.setFreshClaim("inference", key)
	t.Cleanup(func() {
		gate.freezeCell(key)
		gate.clearFreshInferenceClaim(key)
	})
	return run()
}

func liveCapturedCLISelectedClients(value string, present bool) ([]string, error) {
	if !present {
		return []string{"claude", "codex"}, nil
	}
	if value == "" {
		return nil, errors.New("captured CLI client selection is empty")
	}
	clients := strings.Split(value, ",")
	seen := make(map[string]bool, len(clients))
	for _, client := range clients {
		if client == "" || strings.TrimSpace(client) != client {
			return nil, errors.New("captured CLI client selection is malformed")
		}
		if client != "claude" && client != "codex" {
			return nil, errors.New("captured CLI client selection contains an unknown client")
		}
		if seen[client] {
			return nil, errors.New("captured CLI client selection contains a duplicate client")
		}
		seen[client] = true
	}
	return clients, nil
}

func TestLiveCapturedCLISelectedClients(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		present bool
		want    string
		wantErr bool
	}{
		{name: "default both", want: "claude,codex"},
		{name: "Claude only", value: "claude", present: true, want: "claude"},
		{name: "Codex only", value: "codex", present: true, want: "codex"},
		{name: "explicit both", value: "claude,codex", present: true, want: "claude,codex"},
		{name: "reverse order", value: "codex,claude", present: true, want: "codex,claude"},
		{name: "explicit empty", value: "", present: true, wantErr: true},
		{name: "unknown", value: "copilot", present: true, wantErr: true},
		{name: "duplicate", value: "codex,codex", present: true, wantErr: true},
		{name: "empty item", value: "claude,,codex", present: true, wantErr: true},
		{name: "leading separator", value: ",codex", present: true, wantErr: true},
		{name: "trailing separator", value: "claude,", present: true, wantErr: true},
		{name: "whitespace", value: "claude, codex", present: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clients, err := liveCapturedCLISelectedClients(test.value, test.present)
			if (err != nil) != test.wantErr {
				t.Fatalf("liveCapturedCLISelectedClients(%q, %t) error = %v", test.value, test.present, err)
			}
			if err == nil && strings.Join(clients, ",") != test.want {
				t.Fatalf("liveCapturedCLISelectedClients(%q, %t) = %v, want %q", test.value, test.present, clients, test.want)
			}
		})
	}
}

func runLiveCLIWithEffort(t *testing.T, path, client, base, root, prompt, effort string) liveCLIResult {
	t.Helper()
	var args []string
	if client == "claude" {
		args = []string{"--safe-mode", "--print", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "WebSearch,Agent", "--allowedTools", "WebSearch,Agent", "--model", "claude-haiku-5.5", prompt}
		if effort != "" {
			args = append(args[:len(args)-1], append([]string{"--effort", effort}, args[len(args)-1:]...)...)
		}
	} else {
		args = []string{"exec", "--strict-config", "--json", "--ignore-rules", "--skip-git-repo-check", "--sandbox", "read-only", "--model", "gpt-6-luna", "--cd", root, prompt}
		if effort != "" {
			args = append(args[:len(args)-1], append([]string{"--config", "model_reasoning_effort=\"" + effort + "\""}, args[len(args)-1:]...)...)
		}
	}
	stdout := &liveCLIBuffer{}
	stderr := &liveCLIBuffer{}
	command, err := runLiveClient(t, liveClientTimeout, path, args, func(command *exec.Cmd) {
		command.Dir = root
		command.Env = liveCLIEnvironment(client, base, root)
		command.Stdout = stdout
		command.Stderr = stderr
	})
	if captureDir := liveDebugArtifactDirectory(t, "cli-"+client); captureDir != "" {
		for name, body := range map[string][]byte{"stdout.jsonl": stdout.bytes, "stderr.txt": stderr.bytes, "prompt.txt": []byte(prompt)} {
			body = []byte(redact.Text(string(body), liveCopilotClientKey))
			if captureErr := os.WriteFile(filepath.Join(captureDir, name), body, 0600); captureErr != nil {
				t.Fatal("could not retain private CLI diagnostic")
			}
		}
		t.Logf("retained CLI output: %s", captureDir)
	}
	result := parseLiveCLIEvents(client, stdout.bytes)
	if client == "codex" && result.threadID != "" {
		result.verifiedV2Delegation = inspectLiveCodexDelegation(t, root, result.threadID)
	}
	result.errorCode = safeCLIErrorCode(append(append([]byte(nil), stdout.bytes...), stderr.bytes...))
	result.truncated = stdout.truncated || stderr.truncated
	if command.ProcessState != nil {
		result.exitCode = command.ProcessState.ExitCode()
	} else {
		result.exitCode = -1
	}
	result.timedOut = errors.Is(err, context.DeadlineExceeded)
	if err != nil && result.exitCode == 0 {
		result.exitCode = -1
	}
	return result
}

func liveCLIEnvironment(client, base, root string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + root,
		"TMPDIR=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"NO_COLOR=1",
		"CI=1",
		"TERM=dumb",
	}
	if client == "claude" {
		env = append(env, "ANTHROPIC_BASE_URL="+base, "ANTHROPIC_API_KEY="+liveCopilotClientKey, "CLAUDE_CONFIG_DIR="+filepath.Join(root, "claude"), "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	} else {
		env = append(env, "CODEX_HOME="+root, "TASK_PROXY_API_KEY="+liveCopilotClientKey)
	}
	return env
}

func parseLiveCLIEvents(client string, output []byte) liveCLIResult {
	result := liveCLIResult{toolStarted: map[string]int{}, toolCompleted: map[string]int{}, toolFailed: map[string]int{}, toolIDs: map[string]string{}, startedIDs: map[string]string{}, completedIDs: map[string]string{}, spawnedAgents: map[string]bool{}, doneAgents: map[string]bool{}, eventTypes: map[string]int{}, delegatedResultText: map[string][]string{}}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		var event map[string]any
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		typeName, _ := event["type"].(string)
		result.eventTypes[typeName]++
		if client == "claude" {
			parseClaudeEvent(&result, event)
		} else {
			parseCodexEvent(&result, event)
		}
	}
	return result
}

func parseClaudeEvent(result *liveCLIResult, event map[string]any) {
	typeName, _ := event["type"].(string)
	if typeName == "result" {
		isError, _ := event["is_error"].(bool)
		text, _ := event["result"].(string)
		result.final = !isError && strings.TrimSpace(text) != ""
		result.finalOutput = text
		result.sourceURL = result.sourceURL || strings.Contains(text, "https://science.nasa.gov/moon/facts/")
	}
	message, _ := event["message"].(map[string]any)
	content, _ := message["content"].([]any)
	for _, raw := range content {
		block, _ := raw.(map[string]any)
		kind, _ := block["type"].(string)
		if kind == "tool_use" {
			name, _ := block["name"].(string)
			result.toolStarted[name]++
			if id, ok := block["id"].(string); ok {
				result.toolIDs[id] = name
			}
		}
		if kind == "tool_result" {
			name := "tool_result"
			matched := false
			if toolUseID, ok := block["tool_use_id"].(string); ok {
				if toolName, found := result.toolIDs[toolUseID]; found {
					name = toolName
					matched = true
				}
			}
			isError, _ := block["is_error"].(bool)
			if isError {
				result.toolFailed[name]++
			} else {
				result.toolCompleted[name]++
				if matched && (name == "Agent" || name == "Task") {
					result.delegatedResultText[name] = append(result.delegatedResultText[name], claudeToolResultText(block["content"]))
				}
			}
		}
	}
}

func parseCodexEvent(result *liveCLIResult, event map[string]any) {
	typeName, _ := event["type"].(string)
	if typeName == "thread.started" {
		result.threadID, _ = event["thread_id"].(string)
	}
	if typeName == "turn.completed" {
		result.turnCompleted = true
	}
	item, _ := event["item"].(map[string]any)
	if item == nil {
		result.final = result.turnCompleted && result.finalText
		return
	}
	name, _ := item["type"].(string)
	if name == "agent_message" && typeName == "item.completed" {
		text, _ := item["text"].(string)
		result.finalText = result.finalText || strings.TrimSpace(text) != ""
		result.finalOutput = text
		result.sourceURL = result.sourceURL || strings.Contains(text, "https://science.nasa.gov/moon/facts/")
	}
	if name == "collab_tool_call" {
		tool, _ := item["tool"].(string)
		name += ":" + tool
	}
	id, _ := item["id"].(string)
	if typeName == "item.started" {
		result.toolStarted[name]++
		if id != "" {
			result.startedIDs[id] = name
		}
	}
	if typeName == "item.completed" {
		status, _ := item["status"].(string)
		if id != "" && result.startedIDs[id] == name && status != "failed" {
			result.toolCompleted[name]++
			result.completedIDs[id] = name
			if name == "collab_tool_call:spawn_agent" && status == "completed" {
				if receiverIDs, ok := item["receiver_thread_ids"].([]any); ok {
					for _, raw := range receiverIDs {
						if agentID, ok := raw.(string); ok {
							result.spawnedAgents[agentID] = true
						}
					}
				}
			}
			if name == "collab_tool_call:wait" && status == "completed" {
				states, _ := item["agents_states"].(map[string]any)
				for agentID, raw := range states {
					state, _ := raw.(map[string]any)
					if state["status"] == "completed" {
						result.doneAgents[agentID] = true
					}
				}
			}
		}
	}
	if typeName == "item.failed" {
		result.toolFailed[name]++
	}
	result.final = result.turnCompleted && result.finalText
}

func liveSearchCompleted(client string, result liveCLIResult) bool {
	name := "WebSearch"
	if client == "codex" {
		name = "web_search"
	}
	return result.toolStarted[name] > 0 && result.toolCompleted[name] > 0 && result.sourceURL
}

func TestLiveCLIEventEvidence(t *testing.T) {
	ordinary := parseLiveCLIEvents("codex", []byte(`{"type":"item.started","item":{"id":"m1","type":"agent_message"}}
{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"I searched and delegated. https://example.com"}}
{"type":"turn.completed"}`))
	if !ordinary.final || liveSearchCompleted("codex", ordinary) || liveDelegationCompleted("codex", ordinary) {
		t.Fatal("ordinary assistant text was counted as a completed tool")
	}
	webStarted := parseLiveCLIEvents("codex", []byte(`{"type":"item.started","item":{"id":"w1","type":"web_search"}}
{"type":"item.completed","item":{"id":"w2","type":"web_search","query":"moon","action":{"type":"search","query":"moon"}}}
{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"https://example.com"}}
{"type":"turn.completed"}`))
	if liveSearchCompleted("codex", webStarted) {
		t.Fatal("mismatched web search item IDs were counted as completion")
	}
	webCompleted := parseLiveCLIEvents("codex", []byte(`{"type":"item.started","item":{"id":"w1","type":"web_search"}}
{"type":"item.completed","item":{"id":"w1","type":"web_search","query":"moon","action":{"type":"search","query":"moon"}}}
{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"https://science.nasa.gov/moon/facts/"}}
{"type":"turn.completed"}`))
	if !liveSearchCompleted("codex", webCompleted) {
		t.Fatal("completed web search with a source URL was not counted")
	}
	unrelatedSource := parseLiveCLIEvents("codex", []byte(`{"type":"item.started","item":{"id":"w1","type":"web_search"}}
{"type":"item.completed","item":{"id":"w1","type":"web_search","query":"moon","action":{"type":"search","query":"moon"}}}
{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"https://example.com"}}
{"type":"turn.completed"}`))
	if liveSearchCompleted("codex", unrelatedSource) {
		t.Fatal("unrelated URL was counted as the requested NASA source")
	}
	spawnOnly := parseLiveCLIEvents("codex", []byte(`{"type":"item.started","item":{"id":"s1","type":"collab_tool_call","tool":"spawn_agent","status":"in_progress"}}
{"type":"item.completed","item":{"id":"s1","type":"collab_tool_call","tool":"spawn_agent","status":"completed","receiver_thread_ids":["child"]}}
{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"done"}}
{"type":"turn.completed"}`))
	if liveDelegationCompleted("codex", spawnOnly) {
		t.Fatal("spawn alone was counted as completed delegation")
	}
	withWait := parseLiveCLIEvents("codex", []byte(`{"type":"item.started","item":{"id":"s1","type":"collab_tool_call","tool":"spawn_agent","status":"in_progress"}}
{"type":"item.completed","item":{"id":"s1","type":"collab_tool_call","tool":"spawn_agent","status":"completed","receiver_thread_ids":["child"]}}
{"type":"item.started","item":{"id":"wait1","type":"collab_tool_call","tool":"wait","status":"in_progress"}}
{"type":"item.completed","item":{"id":"wait1","type":"collab_tool_call","tool":"wait","status":"completed","agents_states":{"child":{"status":"completed"}}}}
{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"323"}}
{"type":"turn.completed"}`))
	if !liveDelegationCompleted("codex", withWait) {
		t.Fatal("completed child observed through wait was not counted")
	}
	wrongParentAnswer := parseLiveCLIEvents("codex", []byte(`{"type":"item.started","item":{"id":"s1","type":"collab_tool_call","tool":"spawn_agent","status":"in_progress"}}
{"type":"item.completed","item":{"id":"s1","type":"collab_tool_call","tool":"spawn_agent","status":"completed","receiver_thread_ids":["child"]}}
{"type":"item.started","item":{"id":"wait1","type":"collab_tool_call","tool":"wait","status":"in_progress"}}
{"type":"item.completed","item":{"id":"wait1","type":"collab_tool_call","tool":"wait","status":"completed","agents_states":{"child":{"status":"completed"}}}}
{"type":"item.completed","item":{"id":"m1","type":"agent_message","text":"The agent completed."}}
{"type":"turn.completed"}`))
	if liveDelegationCompleted("codex", wrongParentAnswer) {
		t.Fatal("Codex delegation passed without the parent reporting the delegated result")
	}
	claudeTool := parseLiveCLIEvents("claude", []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool1","name":"Task"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool1","is_error":false,"content":[{"type":"text","text":"323"}]}]}}
{"type":"result","is_error":false,"result":"323"}`))
	if !claudeTool.final || !liveDelegationCompleted("claude", claudeTool) || liveSearchCompleted("claude", claudeTool) {
		t.Fatal("Claude tool use/result was not identified precisely")
	}
	claudeWrongChild := parseLiveCLIEvents("claude", []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool1","name":"Agent"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool1","is_error":false,"content":[{"type":"text","text":"322"}]}]}}
{"type":"result","is_error":false,"result":"323"}`))
	if liveDelegationCompleted("claude", claudeWrongChild) {
		t.Fatal("Claude delegation passed with a child result that did not contain the requested answer")
	}
	claudeEmptyChild := parseLiveCLIEvents("claude", []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool1","name":"Task"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool1","is_error":false,"content":[]}]}}
{"type":"result","is_error":false,"result":"323"}`))
	if liveDelegationCompleted("claude", claudeEmptyChild) {
		t.Fatal("Claude delegation passed with an empty child result")
	}
	claudeMissingHandback := parseLiveCLIEvents("claude", []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool1","name":"Agent"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool1","is_error":false,"content":[{"type":"text","text":"323"}]}]}}
{"type":"result","is_error":false,"result":"The agent completed."}`))
	if liveDelegationCompleted("claude", claudeMissingHandback) {
		t.Fatal("Claude delegation passed without the parent handing back the requested answer")
	}
	claudeError := parseLiveCLIEvents("claude", []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tool1","name":"WebSearch"}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tool1","is_error":true}]}}
{"type":"result","is_error":false,"result":"https://example.com"}`))
	if liveSearchCompleted("claude", claudeError) {
		t.Fatal("failed Claude tool result was counted as successful search")
	}
}

func TestLiveCLIRecordedFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		client  string
		fixture string
	}{
		{name: "bare Claude has no search tool", client: "claude", fixture: "claude_bare_search_unavailable.jsonl"},
		{name: "Claude search upstream rejects the request", client: "claude", fixture: "claude_search_upstream_error.jsonl"},
		{name: "Codex claims uninvoked delegation", client: "codex", fixture: "codex_delegation_uninvoked.jsonl"},
		{name: "Codex search cannot correlate mismatched IDs", client: "codex", fixture: "codex_search_different_ids.jsonl"},
		{name: "Codex wait has no spawned child", client: "codex", fixture: "codex_wait_without_spawn.jsonl"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := os.ReadFile(filepath.Join("testdata", test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			result := parseLiveCLIEvents(test.client, output)
			if !result.final || liveSearchCompleted(test.client, result) || liveDelegationCompleted(test.client, result) {
				t.Fatal("recorded client refusal or uninvoked delegation was counted as completed tool work")
			}
		})
	}
}

func TestLiveCLIRecordedClaudeDelegation(t *testing.T) {
	output, err := os.ReadFile(filepath.Join("testdata", "claude_agent_completed.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	result := parseLiveCLIEvents("claude", output)
	if !result.final || !liveDelegationCompleted("claude", result) || liveSearchCompleted("claude", result) {
		t.Fatal("recorded completed Claude child was not identified precisely")
	}
}

func liveDelegationCompleted(client string, result liveCLIResult) bool {
	if client == "claude" {
		for _, name := range []string{"Agent", "Task"} {
			if result.toolStarted[name] == 0 || result.toolCompleted[name] == 0 || !containsDelegatedAnswer(result.finalOutput) {
				continue
			}
			for _, text := range result.delegatedResultText[name] {
				if containsDelegatedAnswer(text) {
					return true
				}
			}
		}
		return false
	}
	if !containsDelegatedAnswer(result.finalOutput) {
		return false
	}
	if result.verifiedV2Delegation && result.toolCompleted["collab_tool_call:wait"] > 0 {
		return true
	}
	if result.toolCompleted["collab_tool_call:spawn_agent"] == 0 || result.toolCompleted["collab_tool_call:wait"] == 0 {
		return false
	}
	for agentID := range result.spawnedAgents {
		if result.doneAgents[agentID] {
			return true
		}
	}
	return false
}

func claudeToolResultText(content any) string {
	if text, ok := content.(string); ok {
		return text
	}
	blocks, _ := content.([]any)
	var text strings.Builder
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		if block["type"] == "text" {
			if part, ok := block["text"].(string); ok {
				text.WriteString(part)
			}
		}
	}
	return text.String()
}

func containsDelegatedAnswer(text string) bool {
	return regexp.MustCompile(`\b323\b`).MatchString(text)
}

func logLiveCLIResult(t *testing.T, client, task string, result liveCLIResult) {
	t.Helper()
	status := "PASS"
	if result.exitCode != 0 || !result.final || result.truncated {
		status = "FAIL"
	}
	if task == "web_search" && !liveSearchCompleted(client, result) {
		status = "FAIL"
	}
	if task == "subagent" && !liveDelegationCompleted(client, result) {
		status = "FAIL"
	}
	t.Logf("task=%s status=%s exit=%d timeout=%t final=%t truncated=%t error_code=%s events=%v tool_started=%v tool_completed=%v tool_failed=%v verified_v2_rollout=%t", task, status, result.exitCode, result.timedOut, result.final, result.truncated, result.errorCode, result.eventTypes, result.toolStarted, result.toolCompleted, result.toolFailed, result.verifiedV2Delegation)
}

func safeCLIErrorCode(output []byte) string {
	if match := regexp.MustCompile(`(?i)\b(?:HTTP[/ ]+|status(?: code)?[/ :=]+)([45][0-9][0-9])\b`).FindSubmatch(output); len(match) == 2 {
		return "http_" + string(match[1])
	}
	for _, code := range []string{"model_not_found", "invalid_request_error", "rate_limit_error", "authentication_error", "permission_error"} {
		if bytes.Contains(bytes.ToLower(output), []byte(code)) {
			return code
		}
	}
	return "unclassified"
}
