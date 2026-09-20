package compare

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestParsePreservesInspectorArguments(t *testing.T) {
	binary, cfg, forwarded, help, err := Parse([]string{"--binary", "/tool", "--config", "db2.toml", "--check-struct-only"})
	if err != nil || help || binary != "/tool" || cfg != "" {
		t.Fatalf("parse = %q %q %q %v %v", binary, cfg, forwarded, help, err)
	}
	if len(forwarded) != 3 || forwarded[0] != "--config" || forwarded[1] != "db2.toml" {
		t.Fatalf("forwarded = %#v", forwarded)
	}
}

func TestRunForwardsArgumentsAndExitCode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("shell script test")
	}
	path := filepath.Join(t.TempDir(), "sync_diff_inspector")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit 9\n"), 0755); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(path, []string{"--config", "db2.toml", "--arg"}, nil, &stdout, &stderr)
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 9 {
		t.Fatalf("exit = %v", err)
	}
	if stdout.String() != "--config\ndb2.toml\n--arg\n" {
		t.Fatalf("arguments = %q", stdout.String())
	}
}
