//go:build !windows

package integration

import (
	"context"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"
)

const liveClientTimeout = 4 * time.Minute
const liveClientStartupTimeout = 15 * time.Second

type liveClientProcessGroup struct {
	pid  int
	kill func(int, syscall.Signal) error

	mu            sync.Mutex
	signalHandled bool
}

func (group *liveClientProcessGroup) cancel() error {
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.signalHandled {
		return nil
	}

	err := group.kill(-group.pid, syscall.SIGKILL)
	if err == nil || err == syscall.ESRCH {
		group.signalHandled = true
	}
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func (group *liveClientProcessGroup) cleanup() error {
	group.mu.Lock()
	defer group.mu.Unlock()

	if group.signalHandled {
		return nil
	}
	err := group.kill(-group.pid, syscall.SIGKILL)
	if err == nil || err == syscall.ESRCH {
		group.signalHandled = true
	}
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func runLiveClient(t *testing.T, timeout time.Duration, path string, args []string, configure func(*exec.Cmd)) (*exec.Cmd, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var processGroupMu sync.Mutex
	var processGroup *liveClientProcessGroup
	getProcessGroup := func() *liveClientProcessGroup {
		processGroupMu.Lock()
		defer processGroupMu.Unlock()
		if processGroup == nil {
			processGroup = &liveClientProcessGroup{pid: command.Process.Pid, kill: syscall.Kill}
		}
		return processGroup
	}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return getProcessGroup().cancel()
	}
	command.WaitDelay = 5 * time.Second
	if configure != nil {
		configure(command)
	}
	runErr := command.Run()
	if command.Process != nil {
		if err := getProcessGroup().cleanup(); err != nil {
			t.Errorf("could not terminate original client process group: %v", err)
		}
	}
	if ctx.Err() != nil {
		return command, ctx.Err()
	}
	return command, runErr
}
