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
	fixture := newNativeHostSourceTaskFixture(t)
	source := fixture.addHostSource(t, "external CLIProxyAPI checkout")

	result := fixture.runTask(t, "prepare-runtime-host", source)
	if result.ExitCode != 0 {
		t.Fatalf("task prepare-runtime-host failed: %s", result.Output)
	}

	calls := fixture.dockerCalls(t)
	if len(calls) != 2 {
		t.Fatalf("expected the build and host preparation Docker calls, got %d: %#v", len(calls), calls)
	}
	assertNativeHostDockerArgs(t, calls[1], fixture, source)
}

func TestNativeHostSourceLinuxTargetMountsRelativePathWithSpaces(t *testing.T) {
	fixture := newNativeHostSourceTaskFixture(t)
	source := fixture.addHostSource(t, "external CLIProxyAPI checkout")
	relativeSource, err := filepath.Rel(fixture.repo, source)
	if err != nil {
		t.Fatalf("compute relative source path: %v", err)
	}

	result := fixture.runTask(t, "test-native", relativeSource)
	if result.ExitCode != 0 {
		t.Fatalf("task test-native failed: %s", result.Output)
	}

	calls := fixture.dockerCalls(t)
	if len(calls) != 1 {
		t.Fatalf("expected one host preparation Docker call, got %d: %#v", len(calls), calls)
	}
	assertNativeHostDockerArgs(t, calls[0], fixture, source)
	if _, err := os.Stat(fixture.runnerCapture); err != nil {
		t.Fatalf("fake integration runner was not invoked: %v", err)
	}
	if got, err := os.ReadFile(fixture.runnerCapture); err != nil || string(got) != "linux/amd64|golang:1-bookworm\n" {
		t.Fatalf("unexpected fake integration runner record %q, error %v", got, err)
	}
}

func TestNativeHostSourceTaskArgumentOverridesInheritedEnvironment(t *testing.T) {
	for _, target := range []string{"prepare-runtime-host", "test-native"} {
		t.Run(target, func(t *testing.T) {
			fixture := newNativeHostSourceTaskFixture(t)
			inherited := fixture.addHostSource(t, "inherited CLIProxyAPI checkout")
			selected := fixture.addHostSource(t, "selected CLIProxyAPI checkout")
			result := fixture.runTask(t, target, inherited, "NATIVE_HOST_SOURCE="+selected)
			if result.ExitCode != 0 {
				t.Fatalf("task %s failed: %s", target, result.Output)
			}
			calls := fixture.dockerCalls(t)
			if len(calls) == 0 {
				t.Fatal("expected host preparation Docker call")
			}
			preparation := calls[len(calls)-1]
			assertNativeHostDockerArgs(t, preparation, fixture, selected)
			if slicesContainsPair(preparation, "-v", canonicalFixturePath(t, inherited)+":/native-host-source:ro") {
				t.Fatalf("inherited host source was mounted instead of CLI override: %#v", preparation)
			}
		})
	}
}

func TestNativeHostSourceDarwinTaskArgumentOverridesInheritedEnvironment(t *testing.T) {
	fixture := newNativeHostSourceTaskFixture(t)
	fixture.system = "Darwin"
	fixture.machine = "arm64"
	inherited := fixture.addHostSource(t, "inherited CLIProxyAPI checkout")
	selected := fixture.addHostSource(t, "selected CLIProxyAPI checkout")
	result := fixture.runTask(t, "test-native", inherited, "NATIVE_HOST_SOURCE="+selected)
	if result.ExitCode != 0 {
		t.Fatalf("task test-native on Darwin failed: %s", result.Output)
	}
	if got, err := os.ReadFile(fixture.preparerSourceCapture); err != nil || string(got) != selected+"\n" {
		t.Fatalf("Darwin preparer source = %q, want %q; error %v", got, selected, err)
	}
}

func TestNativeHostSourcePublishedDefaultOmitsSourceMount(t *testing.T) {
	fixture := newNativeHostSourceTaskFixture(t)
	result := fixture.runTask(t, "test-native", "")
	if result.ExitCode != 0 {
		t.Fatalf("task test-native failed: %s", result.Output)
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
			fixture := newNativeHostSourceTaskFixture(t)
			missing := filepath.Join(fixture.root, "missing CLIProxyAPI checkout")

			result := fixture.runTask(t, target, missing)
			if result.ExitCode == 0 {
				t.Fatalf("task %s accepted a missing source path", target)
			}
			if !strings.Contains(result.Output, "NATIVE_HOST_SOURCE") {
				t.Fatalf("missing-source error did not identify NATIVE_HOST_SOURCE: %s", result.Output)
			}
			if calls := fixture.dockerCalls(t); len(calls) != 0 {
				t.Fatalf("task %s invoked Docker before rejecting the source: %#v", target, calls)
			}
			if _, err := os.Stat(fixture.runnerCapture); !os.IsNotExist(err) {
				t.Fatalf("fake integration runner should not run after source validation failure, stat error: %v", err)
			}
		})
	}
}

func TestTaskPackagePassesImageAndVersionOverrides(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		version string
		image   string
	}{
		{name: "environment", version: "2.0.0", image: "example.test/go:environment"},
		{name: "command line", args: []string{"GO_IMAGE=example.test/go:command", "VERSION=1.2.3.4"}, version: "1.2.3.4", image: "example.test/go:command"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNativeHostSourceTaskFixture(t)
			fixture.image = "example.test/go:environment"
			fixture.version = "2.0.0"
			result := fixture.runTask(t, "package", "", test.args...)
			if result.ExitCode != 0 {
				t.Fatalf("task package failed: %s", result.Output)
			}
			calls := fixture.dockerCalls(t)
			if len(calls) != 1 || !slicesContains(calls[0], test.image) {
				t.Fatalf("build did not use overridden image: %#v", calls)
			}
			if !containsSubstring(calls[0], "-X main.pluginVersion=$VERSION") || !slicesContainsPair(calls[0], "-e", "VERSION="+test.version) {
				t.Fatalf("build did not pass version %q into the container: %#v", test.version, calls)
			}
			if got, err := os.ReadFile(fixture.packageCapture); err != nil || string(got) != test.version+"|linux|amd64\n" {
				t.Fatalf("package arguments = %q, error %v", got, err)
			}
		})
	}
}

func TestTaskPackageVersionTreatsShellSyntaxAsData(t *testing.T) {
	fixture := newNativeHostSourceTaskFixture(t)
	marker := filepath.Join(fixture.root, "unexpected command")
	version := "$(touch '" + marker + "')"
	result := fixture.runTask(t, "package", "", "VERSION="+version)
	if result.ExitCode != 0 {
		t.Fatalf("task package failed: %s", result.Output)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("version executed as shell code; stat error: %v", err)
	}
	calls := fixture.dockerCalls(t)
	if len(calls) != 1 || !slicesContainsPair(calls[0], "-e", "VERSION="+version) {
		t.Fatalf("version did not reach Docker as one argument: %#v", calls)
	}
	if got, err := os.ReadFile(fixture.packageCapture); err != nil || string(got) != version+"|linux|amd64\n" {
		t.Fatalf("package arguments = %q, error %v", got, err)
	}
}

func TestTaskNativeHostUsesImageOverride(t *testing.T) {
	fixture := newNativeHostSourceTaskFixture(t)
	result := fixture.runTask(t, "test-native", "", "GO_IMAGE=example.test/go:fixture")
	if result.ExitCode != 0 {
		t.Fatalf("task test-native failed: %s", result.Output)
	}
	calls := fixture.dockerCalls(t)
	if len(calls) != 1 || !slicesContains(calls[0], "example.test/go:fixture") {
		t.Fatalf("host preparation did not use overridden image: %#v", calls)
	}
	if got, err := os.ReadFile(fixture.runnerCapture); err != nil || string(got) != "linux/amd64|example.test/go:fixture\n" {
		t.Fatalf("native runner image = %q, error %v", got, err)
	}
}

func TestTaskNativeHostDarwinBuildsForHostArchitecture(t *testing.T) {
	for _, test := range []struct{ machine, arch string }{{"x86_64", "amd64"}, {"arm64", "arm64"}} {
		t.Run(test.machine, func(t *testing.T) {
			fixture := newNativeHostSourceTaskFixture(t)
			fixture.system = "Darwin"
			fixture.machine = test.machine
			result := fixture.runTask(t, "test-native", "", "VERSION=1.2.3.4")
			if result.ExitCode != 0 {
				t.Fatalf("task test-native on Darwin failed: %s", result.Output)
			}
			if calls := fixture.dockerCalls(t); len(calls) != 0 {
				t.Fatalf("Darwin native host unexpectedly invoked Docker: %#v", calls)
			}
			wantGo := "darwin/" + test.arch + "|build -buildvcs=false -trimpath -ldflags -X main.pluginVersion=1.2.3.4 -buildmode=c-shared -o build/plugins/darwin/" + test.arch + "/cliproxyapi-copilot.dylib ./cmd/cliproxyapi-copilot\n"
			if got, err := os.ReadFile(fixture.goCapture); err != nil || string(got) != wantGo {
				t.Fatalf("Darwin build = %q, want %q; error %v", got, wantGo, err)
			}
			if got, err := os.ReadFile(fixture.preparerCapture); err != nil || string(got) != "darwin/"+test.arch+"\n" {
				t.Fatalf("Darwin preparer = %q, error %v", got, err)
			}
			if got, err := os.ReadFile(fixture.runnerCapture); err != nil || string(got) != "darwin/"+test.arch+"|golang:1-bookworm\n" {
				t.Fatalf("Darwin runner = %q, error %v", got, err)
			}
		})
	}
}

func TestTaskCleanRemovesReadOnlyModuleCache(t *testing.T) {
	fixture := newNativeHostSourceTaskFixture(t)
	moduleDir := filepath.Join(fixture.repo, ".cache", "go-mod", "example.test", "module@v1.0.0")
	for _, dir := range []string{filepath.Join(fixture.repo, "build"), filepath.Join(fixture.repo, "dist"), moduleDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create cleanup fixture: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture"), []byte("data"), 0o444); err != nil {
			t.Fatalf("write cleanup fixture: %v", err)
		}
	}
	if err := os.Chmod(moduleDir, 0o555); err != nil {
		t.Fatalf("make module cache directory read-only: %v", err)
	}
	result := fixture.runTask(t, "clean", "")
	if result.ExitCode != 0 {
		t.Fatalf("task clean failed: %s", result.Output)
	}
	for _, name := range []string{"build", "dist", ".cache"} {
		if _, err := os.Stat(filepath.Join(fixture.repo, name)); !os.IsNotExist(err) {
			t.Errorf("task clean left %s; stat error: %v", name, err)
		}
	}
}

func TestTaskCleanDoesNotFollowCacheRootSymlinks(t *testing.T) {
	for _, link := range []string{".cache", filepath.Join(".cache", "go-mod")} {
		t.Run(link, func(t *testing.T) {
			fixture := newNativeHostSourceTaskFixture(t)
			outside := filepath.Join(fixture.root, "outside cache")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatalf("create outside cache: %v", err)
			}
			cacheDir := outside
			if link == ".cache" {
				cacheDir = filepath.Join(outside, "go-mod")
			}
			nested := filepath.Join(cacheDir, "nested module")
			if err := os.MkdirAll(nested, 0o755); err != nil {
				t.Fatalf("create outside module cache: %v", err)
			}
			file := filepath.Join(nested, "fixture")
			if err := os.WriteFile(file, []byte("outside data"), 0o444); err != nil {
				t.Fatalf("write outside cache fixture: %v", err)
			}
			if err := os.Chmod(file, 0o444); err != nil {
				t.Fatalf("set outside file permissions: %v", err)
			}
			for _, dir := range []string{outside, cacheDir, nested} {
				if err := os.Chmod(dir, 0o555); err != nil {
					t.Fatalf("make outside cache read-only: %v", err)
				}
			}
			t.Cleanup(func() {
				_ = os.Chmod(outside, 0o755)
				_ = os.Chmod(cacheDir, 0o755)
				_ = os.Chmod(nested, 0o755)
			})
			linkPath := filepath.Join(fixture.repo, link)
			if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
				t.Fatalf("create cache link parent: %v", err)
			}
			if err := os.Symlink(outside, linkPath); err != nil {
				t.Fatalf("create cache symlink: %v", err)
			}
			result := fixture.runTask(t, "clean", "")
			if result.ExitCode != 0 {
				t.Fatalf("task clean failed: %s", result.Output)
			}
			if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
				t.Errorf("task clean left cache link %s; stat error: %v", link, err)
			}
			for _, dir := range []string{outside, cacheDir, nested} {
				if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o555 {
					t.Errorf("outside directory %s permissions changed: info=%v error=%v", dir, info, err)
				}
			}
			if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o444 {
				t.Errorf("outside file permissions changed: info=%v error=%v", info, err)
			}
			if got, err := os.ReadFile(file); err != nil || string(got) != "outside data" {
				t.Errorf("outside file content changed: %q, error %v", got, err)
			}
		})
	}
}

type nativeHostSourceTaskFixture struct {
	root                  string
	system                string
	machine               string
	image                 string
	version               string
	repo                  string
	bin                   string
	dockerCapture         string
	runnerCapture         string
	packageCapture        string
	goCapture             string
	preparerCapture       string
	preparerSourceCapture string
}

func newNativeHostSourceTaskFixture(t *testing.T) *nativeHostSourceTaskFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repository with spaces")
	bin := filepath.Join(root, "fake bin")
	if err := os.MkdirAll(filepath.Join(repo, "packaging"), 0o755); err != nil {
		t.Fatalf("create fixture packaging directory: %v", err)
	}
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
	rootPath := filepath.Join(filepath.Dir(testFile), "..")
	for _, name := range []string{"Taskfile.yaml", "integration/prepare-native-container.sh"} {
		content, err := os.ReadFile(filepath.Join(rootPath, name))
		if err != nil {
			t.Fatalf("read project %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), content, 0o755); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	fixture := &nativeHostSourceTaskFixture{
		root:                  root,
		system:                "Linux",
		machine:               "x86_64",
		image:                 "golang:1-bookworm",
		version:               "0.3.3",
		repo:                  repo,
		bin:                   bin,
		dockerCapture:         filepath.Join(root, "docker arguments"),
		runnerCapture:         filepath.Join(root, "integration runner"),
		packageCapture:        filepath.Join(root, "package arguments"),
		goCapture:             filepath.Join(root, "go arguments"),
		preparerCapture:       filepath.Join(root, "host preparer"),
		preparerSourceCapture: filepath.Join(root, "host source"),
	}
	fixture.writeExecutable(t, filepath.Join(bin, "docker"), "#!/bin/sh\nprintf '%s\\000' \"$@\" >>\"$DOCKER_CAPTURE\"\nprintf '\\n' >>\"$DOCKER_CAPTURE\"\n")
	fixture.writeExecutable(t, filepath.Join(bin, "uname"), "#!/bin/sh\ncase \"${1:-}\" in -s) printf '%s\\n' \"$FAKE_UNAME_OS\" ;; -m) printf '%s\\n' \"$FAKE_UNAME_ARCH\" ;; *) exit 1 ;; esac\n")
	fixture.writeExecutable(t, filepath.Join(bin, "go"), "#!/bin/sh\nprintf '%s/%s|%s\\n' \"$GOOS\" \"$GOARCH\" \"$*\" >>\"$GO_CAPTURE\"\n")
	fixture.writeExecutable(t, filepath.Join(repo, "integration", "prepare-native-host.sh"), "#!/bin/sh\nprintf '%s/%s\\n' \"$NATIVE_HOST_GOOS\" \"$NATIVE_HOST_GOARCH\" >>\"$NATIVE_HOST_PREPARER_CAPTURE\"\nprintf '%s\\n' \"${NATIVE_HOST_SOURCE:-}\" >>\"$NATIVE_HOST_PREPARER_SOURCE_CAPTURE\"\n")
	fixture.writeExecutable(t, filepath.Join(repo, "integration", "test-native-host.sh"), "#!/bin/sh\nprintf '%s|%s\\n' \"$NATIVE_HOST_GOOS/$NATIVE_HOST_GOARCH\" \"$NATIVE_HOST_IMAGE\" >>\"$NATIVE_HOST_RUNNER_CAPTURE\"\n")
	fixture.writeExecutable(t, filepath.Join(repo, "packaging", "package-release.sh"), "#!/bin/sh\nprintf '%s|%s|%s\\n' \"$1\" \"$2\" \"$3\" >>\"$PACKAGE_CAPTURE\"\n")
	return fixture
}

func (f *nativeHostSourceTaskFixture) addHostSource(t *testing.T, name string) string {
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

func (f *nativeHostSourceTaskFixture) writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("write fixture executable %s: %v", path, err)
	}
}

type taskResult struct {
	ExitCode int
	Output   string
}

func (f *nativeHostSourceTaskFixture) runTask(t *testing.T, target, source string, taskArgs ...string) taskResult {
	t.Helper()
	taskPath, err := exec.LookPath("task")
	if err != nil {
		t.Fatalf("find task: %v", err)
	}
	args := append([]string{"--dir", f.repo, target}, taskArgs...)
	command := exec.Command(taskPath, args...)
	command.Env = makeEnvironment(os.Environ(), map[string]string{
		"PATH":                                f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DOCKER_CAPTURE":                      f.dockerCapture,
		"GO_IMAGE":                            f.image,
		"VERSION":                             f.version,
		"NATIVE_HOST_RUNNER_CAPTURE":          f.runnerCapture,
		"PACKAGE_CAPTURE":                     f.packageCapture,
		"GO_CAPTURE":                          f.goCapture,
		"NATIVE_HOST_PREPARER_CAPTURE":        f.preparerCapture,
		"NATIVE_HOST_PREPARER_SOURCE_CAPTURE": f.preparerSourceCapture,
		"FAKE_UNAME_OS":                       f.system,
		"FAKE_UNAME_ARCH":                     f.machine,
		"NATIVE_HOST_SOURCE":                  source,
	})
	output, err := command.CombinedOutput()
	result := taskResult{Output: string(output)}
	if err == nil {
		result.ExitCode = 0
	} else if exitError, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitError.ExitCode()
	} else {
		t.Fatalf("run task %s: %v\n%s", target, err, output)
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

func (f *nativeHostSourceTaskFixture) dockerCalls(t *testing.T) [][]string {
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

func assertNativeHostDockerArgs(t *testing.T, arguments []string, fixture *nativeHostSourceTaskFixture, source string) {
	t.Helper()
	for _, expected := range []string{
		"run",
		"--rm",
		"--platform=linux/amd64",
		"-e", "HOME=/src/.cache/home",
		"-e", "GOENV=off",
		"-e", "GOWORK=off",
		"-e", "GOTOOLCHAIN=auto",
		"-e", "GOCACHE=/src/.cache/go-build",
		"-e", "GOMODCACHE=/src/.cache/go-mod",
		"-e", "NATIVE_HOST_GOOS=linux",
		"-e", "NATIVE_HOST_GOARCH=amd64",
	} {
		if !slicesContains(arguments, expected) {
			t.Errorf("Docker arguments omit %q: %#v", expected, arguments)
		}
	}
	if len(arguments) < 2 || arguments[len(arguments)-2] != "golang:1-bookworm" || arguments[len(arguments)-1] != "integration/prepare-native-host.sh" {
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
