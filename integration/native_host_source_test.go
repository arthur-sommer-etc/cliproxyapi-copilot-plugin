package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeHostSourceRuntimeTargetMountsAbsolutePathWithSpaces(t *testing.T) {
	fixture := newNativeHostSourceMakeFixture(t)
	source := fixture.addHostSource(t, "external CLIProxyAPI checkout")

	result := fixture.runMake(t, "prepare-runtime-host", source)
	if result.ExitCode != 0 {
		t.Fatalf("make prepare-runtime-host failed: %s", result.Output)
	}

	calls := fixture.dockerCalls(t)
	if len(calls) != 2 {
		t.Fatalf("expected the build and host preparation Docker calls, got %d: %#v", len(calls), calls)
	}
	assertNativeHostDockerArgs(t, calls[1], fixture, source)
}

func TestNativeHostSourceLinuxTargetMountsRelativePathWithSpaces(t *testing.T) {
	fixture := newNativeHostSourceMakeFixture(t)
	source := fixture.addHostSource(t, "external CLIProxyAPI checkout")
	relativeSource, err := filepath.Rel(fixture.repo, source)
	if err != nil {
		t.Fatalf("make relative source path: %v", err)
	}

	result := fixture.runMake(t, "test-native", relativeSource)
	if result.ExitCode != 0 {
		t.Fatalf("make test-native failed: %s", result.Output)
	}

	calls := fixture.dockerCalls(t)
	if len(calls) != 1 {
		t.Fatalf("expected one host preparation Docker call, got %d: %#v", len(calls), calls)
	}
	assertNativeHostDockerArgs(t, calls[0], fixture, source)
	if _, err := os.Stat(fixture.runnerCapture); err != nil {
		t.Fatalf("fake integration runner was not invoked: %v", err)
	}
	if got, err := os.ReadFile(fixture.runnerCapture); err != nil || string(got) != "linux/amd64|golang:1.27-bookworm\n" {
		t.Fatalf("unexpected fake integration runner record %q, error %v", got, err)
	}
}

func TestNativeHostSourcePublishedDefaultOmitsSourceMount(t *testing.T) {
	fixture := newNativeHostSourceMakeFixture(t)
	result := fixture.runMake(t, "test-native", "")
	if result.ExitCode != 0 {
		t.Fatalf("make test-native failed: %s", result.Output)
	}

	calls := fixture.dockerCalls(t)
	if len(calls) != 1 {
		t.Fatalf("expected one host preparation Docker call, got %d: %#v", len(calls), calls)
	}
	assertNativeHostDockerArgs(t, calls[0], fixture, "")
}

func TestNativeHostSourceMissingPathFailsBeforeDocker(t *testing.T) {
	for _, target := range []string{"prepare-runtime-host", "test-native"} {
		t.Run(target, func(t *testing.T) {
			fixture := newNativeHostSourceMakeFixture(t)
			missing := filepath.Join(fixture.root, "missing CLIProxyAPI checkout")

			result := fixture.runMake(t, target, missing)
			if result.ExitCode == 0 {
				t.Fatalf("make %s accepted a missing source path", target)
			}
			if !strings.Contains(result.Output, "NATIVE_HOST_SOURCE") {
				t.Fatalf("missing-source error did not identify NATIVE_HOST_SOURCE: %s", result.Output)
			}
			if calls := fixture.dockerCalls(t); len(calls) != 0 {
				t.Fatalf("make %s invoked Docker before rejecting the source: %#v", target, calls)
			}
			if _, err := os.Stat(fixture.runnerCapture); !os.IsNotExist(err) {
				t.Fatalf("fake integration runner should not run after source validation failure, stat error: %v", err)
			}
		})
	}
}

type nativeHostSourceMakeFixture struct {
	root          string
	repo          string
	bin           string
	dockerCapture string
	runnerCapture string
}

func newNativeHostSourceMakeFixture(t *testing.T) *nativeHostSourceMakeFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repository with spaces")
	bin := filepath.Join(root, "fake bin")
	if err := os.MkdirAll(filepath.Join(repo, "integration"), 0o755); err != nil {
		t.Fatalf("create fixture repository: %v", err)
	}
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatalf("create fake command directory: %v", err)
	}

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve native host source test path")
	}
	makefile, err := os.ReadFile(filepath.Join(filepath.Dir(testFile), "..", "Makefile"))
	if err != nil {
		t.Fatalf("read project Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "Makefile"), makefile, 0o644); err != nil {
		t.Fatalf("write fixture Makefile: %v", err)
	}

	fixture := &nativeHostSourceMakeFixture{
		root:          root,
		repo:          repo,
		bin:           bin,
		dockerCapture: filepath.Join(root, "docker arguments"),
		runnerCapture: filepath.Join(root, "integration runner"),
	}
	fixture.writeExecutable(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf '%s\\000' \"$@\" >>\"$DOCKER_CAPTURE\"\nprintf '\\n' >>\"$DOCKER_CAPTURE\"\n")
	fixture.writeExecutable(t, filepath.Join(bin, "uname"), "#!/bin/sh\nif [ \"${1:-}\" = -s ]; then printf 'Linux\\n'; else exec /usr/bin/uname \"$@\"; fi\n")
	fixture.writeExecutable(t, filepath.Join(repo, "integration", "test-native-host.sh"), "#!/bin/sh\nprintf '%s|%s\\n' \"$NATIVE_HOST_GOOS/$NATIVE_HOST_GOARCH\" \"$NATIVE_HOST_IMAGE\" >>\"$NATIVE_HOST_RUNNER_CAPTURE\"\n")
	return fixture
}

func (f *nativeHostSourceMakeFixture) addHostSource(t *testing.T, name string) string {
	t.Helper()
	source := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Join(source, "cmd", "server"), 0o755); err != nil {
		t.Fatalf("create fake host source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.test/host\n\ngo 1.27.0\n"), 0o644); err != nil {
		t.Fatalf("write fake host go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "cmd", "server", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write fake host server entry point: %v", err)
	}
	return source
}

func (f *nativeHostSourceMakeFixture) writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fixture executable %s: %v", path, err)
	}
}

type makeResult struct {
	ExitCode int
	Output   string
}

func (f *nativeHostSourceMakeFixture) runMake(t *testing.T, target, source string) makeResult {
	t.Helper()
	makePath, err := exec.LookPath("make")
	if err != nil {
		t.Fatalf("find make: %v", err)
	}
	command := exec.Command(makePath, "--no-print-directory", "-C", f.repo, target)
	command.Env = makeEnvironment(os.Environ(), map[string]string{
		"PATH":                       f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_CAPTURE":             f.dockerCapture,
		"GO_IMAGE":                   "golang:1.27-bookworm",
		"NATIVE_HOST_RUNNER_CAPTURE": f.runnerCapture,
		"NATIVE_HOST_SOURCE":         source,
		"MAKEFLAGS":                  "",
		"MAKELEVEL":                  "",
	})
	output, err := command.CombinedOutput()
	result := makeResult{Output: string(output)}
	if err == nil {
		result.ExitCode = 0
	} else if exitError, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitError.ExitCode()
	} else {
		t.Fatalf("run make %s: %v\n%s", target, err, output)
	}
	return result
}

func makeEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, replaced := replacements[name]; !replaced {
			result = append(result, entry)
		}
	}
	for name, value := range replacements {
		result = append(result, name+"="+value)
	}
	return result
}

func (f *nativeHostSourceMakeFixture) dockerCalls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(f.dockerCapture)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read captured Docker arguments: %v", err)
	}

	var calls [][]string
	for _, record := range bytes.Split(data, []byte{'\n'}) {
		if len(record) == 0 {
			continue
		}
		arguments := bytes.Split(record, []byte{0})
		if len(arguments) > 0 && len(arguments[len(arguments)-1]) == 0 {
			arguments = arguments[:len(arguments)-1]
		}
		call := make([]string, len(arguments))
		for index, argument := range arguments {
			call[index] = string(argument)
		}
		calls = append(calls, call)
	}
	return calls
}

func assertNativeHostDockerArgs(t *testing.T, arguments []string, fixture *nativeHostSourceMakeFixture, source string) {
	t.Helper()
	for _, expected := range []string{
		"run",
		"--rm",
		"--platform=linux/amd64",
		"-e", "HOME=/src/.cache/home",
		"-e", "GOENV=off",
		"-e", "GOWORK=off",
		"-e", "GOTOOLCHAIN=local",
		"-e", "GOCACHE=/src/.cache/go-build",
		"-e", "GOMODCACHE=/src/.cache/go-mod",
		"-e", "NATIVE_HOST_GOOS=linux",
		"-e", "NATIVE_HOST_GOARCH=amd64",
	} {
		if !slicesContains(arguments, expected) {
			t.Errorf("Docker arguments omit %q: %#v", expected, arguments)
		}
	}
	if len(arguments) < 2 || arguments[len(arguments)-2] != "golang:1.27-bookworm" || arguments[len(arguments)-1] != "integration/prepare-native-host.sh" {
		t.Errorf("Docker image or host preparation command changed: %#v", arguments)
	}
	if !slicesContainsPair(arguments, "-w", "/src") {
		t.Errorf("Docker working directory is not /src: %#v", arguments)
	}
	repositoryPath := canonicalFixturePath(t, fixture.repo)
	if !slicesContainsPair(arguments, "-v", repositoryPath+":/src") {
		t.Errorf("repository mount did not preserve its path: %#v", arguments)
	}
	if source == "" {
		if slicesContains(arguments, "NATIVE_HOST_SOURCE=/native-host-source") || containsSubstring(arguments, "/native-host-source") {
			t.Errorf("published default unexpectedly added a local source mount: %#v", arguments)
		}
		return
	}
	absoluteSource := canonicalFixturePath(t, source)
	if !slicesContainsPair(arguments, "-e", "NATIVE_HOST_SOURCE=/native-host-source") {
		t.Errorf("Docker environment does not select the mounted source: %#v", arguments)
	}
	if !slicesContainsPair(arguments, "-v", absoluteSource+":/native-host-source:ro") {
		t.Errorf("local source was not mounted read-only as one argument: %#v", arguments)
	}
}

func canonicalFixturePath(t *testing.T, path string) string {
	t.Helper()
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve fixture path %q: %v", path, err)
	}
	return canonicalPath
}

func slicesContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func slicesContainsPair(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, fragment string) bool {
	for _, value := range values {
		if strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}
