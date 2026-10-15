//go:build !windows

package integration

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLiveClientProcessGroupSignalLifecycle(t *testing.T) {
	t.Run("successful cancellation skips final cleanup signal", func(t *testing.T) {
		var signals []int
		group := &liveClientProcessGroup{pid: 42, kill: func(pid int, signal syscall.Signal) error {
			if pid != -42 || signal != syscall.SIGKILL {
				t.Fatalf("kill(%d, %v), want kill(-42, SIGKILL)", pid, signal)
			}
			signals = append(signals, pid)
			return nil
		}}

		if err := group.cancel(); err != nil {
			t.Fatalf("cancel process group: %v", err)
		}
		if err := group.cleanup(); err != nil {
			t.Fatalf("cleanup process group: %v", err)
		}
		if len(signals) != 1 {
			t.Fatalf("process group signals = %d, want one cancellation signal", len(signals))
		}
	})

	t.Run("failed cancellation retains final cleanup", func(t *testing.T) {
		var signals int
		group := &liveClientProcessGroup{pid: 42, kill: func(int, syscall.Signal) error {
			signals++
			if signals == 1 {
				return syscall.EPERM
			}
			return nil
		}}

		if err := group.cancel(); !errors.Is(err, syscall.EPERM) {
			t.Fatalf("cancel process group error = %v, want EPERM", err)
		}
		if err := group.cleanup(); err != nil {
			t.Fatalf("cleanup process group: %v", err)
		}
		if signals != 2 {
			t.Fatalf("process group signals = %d, want cancellation attempt and final cleanup", signals)
		}
	})

	t.Run("normal exit retains descendant cleanup", func(t *testing.T) {
		signals := 0
		group := &liveClientProcessGroup{pid: 42, kill: func(int, syscall.Signal) error {
			signals++
			return nil
		}}

		if err := group.cleanup(); err != nil {
			t.Fatalf("cleanup process group: %v", err)
		}
		if signals != 1 {
			t.Fatalf("process group signals = %d, want one final cleanup signal", signals)
		}
	})
}

func TestRunLiveClientKillsStalledProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "descendant-survived")
	_, err := runLiveClient(t, 100*time.Millisecond, "/bin/sh", []string{"-c", "(sleep 0.4; touch \"$1\") & wait", "stalled-client", marker}, nil)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("runLiveClient error = %v, want deadline exceeded", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stalled client descendant survived timeout, marker stat error = %v", err)
	}
}

func TestOriginalCodexDriverTimesOutAndReapsStalledAppServer(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("original Codex harness requires macOS sandbox-exec")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "late-marker")
	fakeBinary := filepath.Join(root, "fake-codex")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.0.1'; exit 0; fi\n(sleep 0.5; touch '" + marker + "') &\nsleep 30\n"
	if err := os.WriteFile(fakeBinary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalog, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "sandbox.sb")
	if err := os.WriteFile(profile, []byte("(version 1)\n(deny network*)\n(allow default)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate integration test source")
	}
	driver := filepath.Join(filepath.Dir(source), "original_codex_image.py")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	_, runErr := runLiveClient(t, 10*time.Second, python, []string{driver, "--base-url", "http://127.0.0.1:12345/v1", "--output-root", root, "--image-url", "https://images.invalid/test.png", "--rpc-timeout", "0.1", "--supervised"}, func(command *exec.Cmd) {
		command.Env = append(os.Environ(), "CPA_LIVE_CODEX_BINARY="+fakeBinary, "CPA_LIVE_CODEX_EXPECTED_VERSION=0.0.1", "CPA_LIVE_CODEX_CATALOG="+catalog, "CPA_LIVE_CODEX_SANDBOX="+profile, "TASK_PROXY_API_KEY=fake-test-key")
		command.Stdout, command.Stderr = &stdout, &stderr
	})
	if runErr == nil {
		t.Fatalf("stalled app-server driver succeeded, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"error": "rpc_timeout"`) {
		t.Fatalf("driver did not report its RPC deadline, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stalled app-server descendant survived driver cleanup, marker stat error = %v", err)
	}
}

func TestOuterTimeoutKillsSupervisedOriginalCodexProcessTree(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("original Codex harness requires macOS sandbox-exec")
	}
	root := t.TempDir()
	started := filepath.Join(root, "app-server-started")
	marker := filepath.Join(root, "late-descendant-marker")
	fakeBinary := filepath.Join(root, "fake-codex")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.0.1'; exit 0; fi\n(touch '" + started + "'; sleep 2.5; touch '" + marker + "') &\nsleep 30\n"
	if err := os.WriteFile(fakeBinary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalog, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "sandbox.sb")
	if err := os.WriteFile(profile, []byte("(version 1)\n(deny network*)\n(allow default)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate integration test source")
	}
	driver := filepath.Join(filepath.Dir(source), "original_codex_image.py")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	result := make(chan error, 1)
	go func() {
		_, runErr := runLiveClient(t, 2*time.Second, python, []string{driver, "--base-url", "http://127.0.0.1:12345/v1", "--output-root", root, "--image-url", "https://images.invalid/test.png", "--rpc-timeout", "30", "--supervised"}, func(command *exec.Cmd) {
			command.Env = append(os.Environ(), "CPA_LIVE_CODEX_BINARY="+fakeBinary, "CPA_LIVE_CODEX_EXPECTED_VERSION=0.0.1", "CPA_LIVE_CODEX_CATALOG="+catalog, "CPA_LIVE_CODEX_SANDBOX="+profile, "TASK_PROXY_API_KEY=fake-test-key")
			command.Stdout, command.Stderr = &stdout, &stderr
		})
		result <- runErr
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("outer timeout fired before app-server started: %v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		case <-deadline:
			err := <-result
			t.Fatalf("app-server descendant did not start: %v, stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("runLiveClient error = %v, want outer deadline exceeded", err)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("app-server descendant survived outer timeout, marker stat error = %v", err)
	}
}

func TestOriginalCodexDriverTimesOutDespiteUnrelatedNotifications(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("original Codex harness requires macOS sandbox-exec")
	}
	root := t.TempDir()
	marker := filepath.Join(root, "late-turn-marker")
	fakeBinary := filepath.Join(root, "fake-codex")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo 'codex-cli 0.0.1'; exit 0; fi\nwhile IFS= read -r line; do\ncase \"$line\" in\n*'\"method\": \"initialize\"'*) printf '{\"id\":1,\"result\":{}}\\n' ;;\n*'\"method\": \"thread/start\"'*) printf '{\"id\":2,\"result\":{\"model\":\"gpt-6-luna\",\"thread\":{\"id\":\"thread-1\"}}}\\n' ;;\n*'\"method\": \"turn/start\"'*) printf '{\"id\":3,\"result\":{\"turn\":{\"id\":\"turn-1\"}}}\\n'; (sleep 0.5; touch '" + marker + "') & while :; do printf '{\"method\":\"unrelated/progress\",\"params\":{}}\\n'; sleep 0.02; done ;;\nesac\ndone\n"
	if err := os.WriteFile(fakeBinary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalog, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "sandbox.sb")
	if err := os.WriteFile(profile, []byte("(version 1)\n(deny network*)\n(allow default)\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate integration test source")
	}
	driver := filepath.Join(filepath.Dir(source), "original_codex_image.py")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	_, runErr := runLiveClient(t, 10*time.Second, python, []string{driver, "--base-url", "http://127.0.0.1:12345/v1", "--output-root", root, "--image-url", "https://images.invalid/test.png", "--rpc-timeout", "1", "--turn-timeout", "0.1", "--supervised"}, func(command *exec.Cmd) {
		command.Env = append(os.Environ(), "CPA_LIVE_CODEX_BINARY="+fakeBinary, "CPA_LIVE_CODEX_EXPECTED_VERSION=0.0.1", "CPA_LIVE_CODEX_CATALOG="+catalog, "CPA_LIVE_CODEX_SANDBOX="+profile, "TASK_PROXY_API_KEY=fake-test-key")
		command.Stdout, command.Stderr = &stdout, &stderr
	})
	if runErr == nil {
		t.Fatalf("stalled turn driver succeeded, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"error": "turn_timeout"`) {
		t.Fatalf("driver did not report its turn deadline, stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stalled turn descendant survived driver cleanup, marker stat error = %v", err)
	}
}
