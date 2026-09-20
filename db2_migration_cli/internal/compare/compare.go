// Package compare starts sync_diff_inspector as an external program.
package compare

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pingcap/db2-migration-cli/internal/config"
)

type Command struct {
	Path string
	Args []string
}

func Usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: db2-migrate compare [--binary PATH] [--cli-config FILE] [sync_diff_inspector arguments...]")
}

// Parse preserves every argument that does not belong to the wrapper.
func Parse(args []string) (binary, cliConfig string, forwarded []string, help bool, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "help", "-h", "--help":
			if len(args) == 1 {
				return "", "", nil, true, nil
			}
			forwarded = append(forwarded, args[i])
		case "--binary":
			if i+1 == len(args) {
				return "", "", nil, false, fmt.Errorf("--binary requires a value")
			}
			i++
			binary = args[i]
		case "--cli-config":
			if i+1 == len(args) {
				return "", "", nil, false, fmt.Errorf("--cli-config requires a value")
			}
			i++
			cliConfig = args[i]
		default:
			forwarded = append(forwarded, args[i])
		}
	}
	return
}

func Resolve(binary string, cfg config.File) (string, error) {
	for _, candidate := range []string{binary, cfg.Tools.SyncDiffBinary, os.Getenv("SYNC_DIFF_INSPECTOR")} {
		if candidate == "" {
			continue
		}
		return executable(candidate)
	}
	path, err := exec.LookPath("sync_diff_inspector")
	if err != nil {
		return "", fmt.Errorf("sync_diff_inspector not found; pass --binary, set tools.sync-diff-binary or SYNC_DIFF_INSPECTOR, or add it to PATH")
	}
	return executable(path)
}

func executable(candidate string) (string, error) {
	if !strings.ContainsAny(candidate, `/\\`) {
		path, err := exec.LookPath(candidate)
		if err != nil {
			return "", fmt.Errorf("executable %q not found: %w", candidate, err)
		}
		candidate = path
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("cannot use %q: %w", abs, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("%q must be a regular executable file", abs)
	}
	return abs, nil
}

func Run(path string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}
