package lightning

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/pingcap/db2-migration-cli/internal/config"
)

func executable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestCreateProducesParseableCompleteTOML(t *testing.T) {
	t.Setenv("LIGHTNING_TEST_PASSWORD", "not-used-here")
	path := filepath.Join(t.TempDir(), "task.toml")
	created, err := Create(path, `D:\db2-export`, "127.0.0.1", 4000, "root", "secret", 10080, "tidb", "", "", "lightning.log", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(created)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if _, err := toml.Decode(string(b), &parsed); err != nil {
		t.Fatalf("invalid TOML: %v\n%s", err, b)
	}
	for _, section := range []string{"lightning", "security", "checkpoint", "conflict", "tikv-importer", "mydumper", "tidb", "post-restore", "cron"} {
		if _, ok := parsed[section]; !ok {
			t.Errorf("missing [%s]", section)
		}
	}
	if !strings.Contains(string(b), "[mydumper.csv]") || !strings.Contains(string(b), "[tidb.security]") {
		t.Fatalf("missing nested sections:\n%s", b)
	}
	if !strings.Contains(string(b), "/mnt/d/db2-export") {
		t.Fatalf("Windows path was not converted for WSL: %s", b)
	}
}

func TestCreateLocalValidation(t *testing.T) {
	_, err := Create(filepath.Join(t.TempDir(), "task.toml"), "/data", "host", 4000, "root", "", 10080, "local", "", "", "log", false)
	if err == nil || !strings.Contains(err.Error(), "--pd-addr") {
		t.Fatalf("error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "task.toml")
	if _, err := Create(path, "/data", "host", 4000, "root", "", 10080, "local", "pd:2379", "sorted", "log", false); err != nil {
		t.Fatal(err)
	}
}

func TestRedactAndStatus(t *testing.T) {
	if got := Redact("password = \"secret\"\nuser = \"root\"\n"); strings.Contains(got, "secret") || !strings.Contains(got, `password = "******"`) {
		t.Fatalf("redaction = %q", got)
	}
	log := filepath.Join(t.TempDir(), "lightning.log")
	cases := []struct{ content, want string }{
		{"the whole procedure completed\ntidb lightning exit", "succeeded"},
		{"tidb lightning exit", "failed-or-incomplete"},
		{"still importing", "running-or-interrupted"},
	}
	if got, err := Status(log); err != nil || got != "not-started" {
		t.Fatalf("missing log = %q, %v", got, err)
	}
	for _, tt := range cases {
		if err := os.WriteFile(log, []byte(tt.content), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := Status(log)
		if err != nil || got != tt.want {
			t.Errorf("%q -> %q, %v; want %q", tt.content, got, err, tt.want)
		}
	}
}

func TestResolvePriorityAndToolkitDiscovery(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("WSL/Linux executable semantics")
	}
	temp := t.TempDir()
	explicit := filepath.Join(temp, "explicit-lightning")
	executable(t, explicit, "exit 0")
	nested := filepath.Join(temp, "toolkit", "tidb-community-toolkit-x-linux-amd64", "tidb-lightning")
	if err := os.MkdirAll(filepath.Dir(nested), 0755); err != nil {
		t.Fatal(err)
	}
	executable(t, nested, "exit 0")
	inv, err := Resolve(explicit, filepath.Join(temp, "toolkit"), config.File{})
	if err != nil || inv.Path != explicit {
		t.Fatalf("--binary priority: %+v, %v", inv, err)
	}
	inv, err = Resolve("", filepath.Join(temp, "toolkit"), config.File{})
	if err != nil || inv.Path != nested {
		t.Fatalf("nested toolkit: %+v, %v", inv, err)
	}
	t.Setenv("TIDB_TOOLKIT_DIR", filepath.Join(temp, "toolkit"))
	inv, err = Resolve("", "", config.File{})
	if err != nil || inv.Path != nested {
		t.Fatalf("environment toolkit: %+v, %v", inv, err)
	}
}

func TestFindToolkitRejectsAmbiguousAndNonExecutable(t *testing.T) {
	temp := t.TempDir()
	for _, name := range []string{"a/tidb-lightning", "b/tidb-lightning"} {
		path := filepath.Join(temp, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		executable(t, path, "exit 0")
	}
	if _, err := findToolkit(temp); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguous error = %v", err)
	}
	bad := t.TempDir()
	path := filepath.Join(bad, "tidb-lightning")
	if err := os.WriteFile(path, []byte("not executable"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := findToolkit(bad); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("non-executable error = %v", err)
	}
	exeDir := t.TempDir()
	exe := filepath.Join(exeDir, "tidb-lightning.exe")
	executable(t, exe, "exit 0")
	if _, err := findToolkit(exeDir); err == nil || !strings.Contains(err.Error(), "Windows .exe") {
		t.Fatalf(".exe error = %v", err)
	}
}

func TestRunForwardsArgumentsAndExitCode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("shell script test")
	}
	temp := t.TempDir()
	binary := filepath.Join(temp, "tidb-lightning")
	executable(t, binary, "printf '%s\\n' \"$@\"; exit 17")
	var out, errOut bytes.Buffer
	err := Run(Invocation{Path: binary}, "task.toml", []string{"--log-level", "debug"}, nil, &out, &errOut)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
		t.Fatalf("exit = %v", err)
	}
	if got := out.String(); got != "--config\ntask.toml\n--log-level\ndebug\n" {
		t.Fatalf("arguments = %q", got)
	}
}
