package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pingcap/db2-migration-cli/internal/compare"
	"github.com/pingcap/db2-migration-cli/internal/config"
	"github.com/pingcap/db2-migration-cli/internal/lightning"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(stdout)
		return 0
	}
	switch args[0] {
	case "compare":
		return runCompare(args[1:], stdin, stdout, stderr)
	case "lightning":
		return runLightning(args[1:], stdin, stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown command %q\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: db2-migrate <compare|lightning> [options]")
	fmt.Fprintln(w, "  compare    invoke sync_diff_inspector as an external executable")
	fmt.Fprintln(w, "  lightning  manage a TiDB Lightning task configuration and invocation")
}
func runCompare(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	binary, configPath, forwarded, help, err := compare.Parse(args)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	if help {
		compare.Usage(stdout)
		return 0
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	path, err := compare.Resolve(binary, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if err := compare.Run(path, forwarded, stdin, stdout, stderr); err != nil {
		return processCode(err, stderr)
	}
	return 0
}

func runLightning(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		lightning.Usage(stdout)
		return 0
	}
	switch args[0] {
	case "init":
		return lightningInit(args[1:], stdout, stderr)
	case "run":
		return lightningRun(args[1:], stdin, stdout, stderr)
	case "show":
		return lightningShow(args[1:], stdout, stderr)
	case "status":
		return lightningStatus(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "unknown lightning command %q\n", args[0])
	lightning.Usage(stderr)
	return 2
}

func lightningInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lightning init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", lightning.DefaultConfig, "task TOML to create")
	data := fs.String("data-dir", "", "input directory or external-storage URI")
	host := fs.String("host", "127.0.0.1", "TiDB host")
	port := fs.Int("port", 4000, "TiDB SQL port")
	user := fs.String("user", "root", "TiDB user")
	status := fs.Int("status-port", 10080, "TiDB status port")
	backend := fs.String("backend", "tidb", "tidb or local")
	pd := fs.String("pd-addr", "", "PD addresses for local backend")
	sorted := fs.String("sorted-kv-dir", "", "local backend temporary directory")
	log := fs.String("log-file", "tidb-lightning.log", "Lightning log file")
	passwordEnv := fs.String("password-env", "", "environment variable containing the TiDB password")
	force := fs.Bool("force", false, "overwrite an existing task TOML")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "error: unexpected argument", fs.Arg(0))
		return 2
	}
	password := ""
	if *passwordEnv != "" {
		var ok bool
		password, ok = os.LookupEnv(*passwordEnv)
		if !ok {
			fmt.Fprintf(stderr, "error: environment variable %s is not set\n", *passwordEnv)
			return 1
		}
	}
	created, err := lightning.Create(*path, *data, *host, *port, *user, password, *status, strings.ToLower(*backend), *pd, *sorted, *log, *force)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintln(stdout, "created", created)
	return 0
}

func lightningRun(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	configPath, binary, toolkit, cliConfig, extra, help, err := parseRun(args)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	if help {
		fmt.Fprintln(stdout, "Usage: db2-migrate lightning run [--config TASK] [--binary PATH] [--toolkit-dir DIR] [--cli-config FILE] [TiDB Lightning options]")
		return 0
	}
	cfg, err := config.Load(cliConfig)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	inv, err := lightning.Resolve(binary, toolkit, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if err := lightning.Run(inv, configPath, extra, stdin, stdout, stderr); err != nil {
		return processCode(err, stderr)
	}
	return 0
}

func parseRun(args []string) (configPath, binary, toolkit, cliConfig string, extra []string, help bool, err error) {
	configPath = lightning.DefaultConfig
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "help" || a == "-h" || a == "--help") && len(args) == 1 {
			help = true
			return
		}
		switch a {
		case "--config", "--binary", "--toolkit-dir", "--cli-config":
			if i+1 == len(args) {
				err = fmt.Errorf("%s requires a value", a)
				return
			}
			i++
			switch a {
			case "--config":
				configPath = args[i]
			case "--binary":
				binary = args[i]
			case "--toolkit-dir":
				toolkit = args[i]
			case "--cli-config":
				cliConfig = args[i]
			}
		default:
			extra = append(extra, a)
		}
	}
	return
}

func lightningShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lightning show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", lightning.DefaultConfig, "task TOML")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return 2
	}
	b, err := os.ReadFile(*path)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprint(stdout, lightning.Redact(string(b)))
	return 0
}
func lightningStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("lightning status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", lightning.DefaultConfig, "task TOML")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, abs, err := lightning.Load(*path)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	log := cfg.Lightning.File
	if log == "" {
		fmt.Fprintln(stderr, "error: [lightning].file is empty")
		return 1
	}
	if !filepath.IsAbs(log) {
		log = filepath.Join(filepath.Dir(abs), log)
	}
	status, err := lightning.Status(log)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprintln(stdout, status)
	return 0
}

func processCode(err error, stderr io.Writer) int {
	type exitCoder interface{ ExitCode() int }
	var e interface {
		error
		exitCoder
	}
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	fmt.Fprintln(stderr, "error:", err)
	return 1
}
