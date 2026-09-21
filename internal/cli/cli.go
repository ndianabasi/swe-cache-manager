package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
	"github.com/ndianabasi/swe-cache-manager/internal/diagnostic"
	"github.com/ndianabasi/swe-cache-manager/internal/gitcache"
	"github.com/ndianabasi/swe-cache-manager/internal/service"
)

const Version = "0.3.0"

func Run(args []string, out, errOut io.Writer) int {
	return runWithReadme(context.Background(), args, out, errOut, "")
}

// RunWithReadme handles a command and supplies the documentation embedded by
// the executable's top-level package. Keeping the CLI package independent of
// the repository root makes its command parsing directly testable.
func RunWithReadme(args []string, out, errOut io.Writer, readme string) int {
	return runWithReadme(context.Background(), args, out, errOut, readme)
}

// RunWithReadmeContext handles a command with cancellation support. The
// executable uses it so Ctrl-C cancels Git cleanly and releases its cache lock.
func RunWithReadmeContext(ctx context.Context, args []string, out, errOut io.Writer, readme string) int {
	return runWithReadme(ctx, args, out, errOut, readme)
}

func runWithReadme(ctx context.Context, args []string, out, errOut io.Writer, readme string) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		usage(out)
		return 0
	}
	if args[0] == "--readme" {
		if len(args) != 1 {
			fmt.Fprintln(errOut, "--readme cannot be combined with a command")
			return 2
		}
		fmt.Fprint(out, readme)
		return 0
	}
	if args[0] == "help" {
		if len(args) == 1 {
			usage(out)
			return 0
		}
		return commandUsage(args[1:], out, errOut)
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintf(out, "swe-cache %s\n", Version)
		return 0
	}
	if args[0] == "init" {
		if asksForHelp(args[1:]) {
			initUsage(out)
			return 0
		}
		return initConfig(args[1:], out, errOut)
	}
	if args[0] == "start" || args[0] == "stop" || args[0] == "restart" {
		if asksForHelp(args[1:]) {
			lifecycleUsage(args[0], out)
			return 0
		}
		return lifecycle(args[0], args[1:], out, errOut)
	}
	if args[0] == "git" {
		return gitCommand(ctx, args[1:], out, errOut)
	}
	if args[0] == "oci" {
		return ociCommand(ctx, args[1:], out, errOut)
	}
	if args[0] == "status" || args[0] == "doctor" || args[0] == "stats" {
		if asksForHelp(args[1:]) {
			inspectUsage(args[0], out)
			return 0
		}
		return inspectCommand(args[0], args[1:], out, errOut)
	}
	if args[0] == "gc" {
		if asksForHelp(args[1:]) {
			gcUsage(out)
			return 0
		}
		return gcCommand(args[1:], out, errOut)
	}
	if isKnown(args[0]) {
		fmt.Fprintf(errOut, "%s: not implemented yet\n", args[0])
		return 3
	}
	fmt.Fprintf(errOut, "unknown command %q\n", args[0])
	usage(errOut)
	return 2
}

func inspectCommand(command string, args []string, out, errOut io.Writer) int {
	root, ok := rootOption(args, errOut)
	if !ok {
		return 2
	}
	c, err := loadConfig(root)
	if err != nil {
		fmt.Fprintf(errOut, "load configuration: %v\n", err)
		return 1
	}
	ctx := context.Background()
	switch command {
	case "status":
		r := diagnostic.Status(ctx, c, service.CommandRunner{})
		renderStatus(out, c, r)
	case "doctor":
		r := diagnostic.Doctor(ctx, c, service.CommandRunner{})
		fmt.Fprintf(out, "docker: %s\ngit: %s\nservice container: %s\napt-cacher-ng: %s\nzot: %s\nverdaccio: %s\nathens: %s\n", r.Docker, r.Git, r.Container, r.APT, r.OCI, r.NPM, r.Go)
		for _, name := range []string{"apt", "oci", "git", "npm", "go"} {
			fmt.Fprintf(out, "%s path: %s\n", name, r.Paths[name])
		}
		if r.Docker != "available" || r.Git != "available" {
			return 1
		}
	case "stats":
		stats, err := diagnostic.CollectStats(c)
		if err != nil {
			fmt.Fprintf(errOut, "collect stats: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "apt bytes: %d\noci bytes: %d\ngit bytes: %d\nnpm bytes: %d\ngo bytes: %d\ngit mirrors: %d\n", stats.APTBytes, stats.OCIBytes, stats.GitBytes, stats.NPMBytes, stats.GoBytes, stats.GitMirrors)
	}
	return 0
}

func gcCommand(args []string, out, errOut io.Writer) int {
	root := ""
	if len(args) >= 2 && args[0] == "--root" {
		root, args = args[1], args[2:]
	}
	if len(args) != 1 || (args[0] != "git" && args[0] != "apt" && args[0] != "oci" && args[0] != "npm" && args[0] != "go" && args[0] != "--all") {
		fmt.Fprintln(errOut, "usage: swe-cache gc [--root PATH] {apt|oci|git|npm|go|--all}")
		return 2
	}
	c, err := loadConfig(root)
	if err != nil {
		fmt.Fprintf(errOut, "load configuration: %v\n", err)
		return 1
	}
	if args[0] == "git" || args[0] == "--all" {
		count := 0
		err = filepath.WalkDir(c.GitDir(), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() || !strings.HasSuffix(entry.Name(), ".git") {
				return nil
			}
			count++
			_, commandErr := (service.CommandRunner{}).Run(context.Background(), "git", "-C", path, "repack", "-d")
			if commandErr != nil {
				return fmt.Errorf("repack %s: %w", path, commandErr)
			}
			return filepath.SkipDir
		})
		if err != nil {
			fmt.Fprintf(errOut, "gc git: %v\n", err)
			return 1
		}
		fmt.Fprintf(out, "repacked %d Git mirrors; no objects were pruned\n", count)
	}
	if args[0] == "apt" || args[0] == "oci" || args[0] == "npm" || args[0] == "go" || args[0] == "--all" {
		fmt.Fprintln(out, "APT, OCI, npm, and Go retention are service-managed; no cache entries were deleted")
	}
	return 0
}

func gitCommand(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 || asksForHelp(args) || (args[0] == "clone" && asksForHelp(args[1:])) {
		gitCloneUsage(out)
		return 0
	}
	if args[0] != "clone" {
		fmt.Fprintln(errOut, "unknown git command; use: swe-cache git clone [--root PATH] [--commit SHA] [--force] URL DESTINATION")
		return 2
	}
	args = args[1:]
	root, commit := "", ""
	force := false
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--force":
			force = true
			args = args[1:]
			continue
		case "--root":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--root requires a value")
				return 2
			}
			root = args[1]
		case "--commit":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--commit requires a value")
				return 2
			}
			commit = args[1]
		default:
			fmt.Fprintf(errOut, "unknown git clone option %q\n", args[0])
			return 2
		}
		args = args[2:]
	}
	if len(args) != 2 {
		fmt.Fprintln(errOut, "usage: swe-cache git clone [--root PATH] [--commit SHA] [--force] URL DESTINATION")
		return 2
	}
	loaded, err := loadConfig(root)
	if err != nil {
		fmt.Fprintf(errOut, "load configuration: %v\n", err)
		return 1
	}
	if !loaded.Git.Enabled {
		fmt.Fprintln(errOut, "Git caching is disabled in configuration")
		return 1
	}
	m := gitcache.Manager{
		Root:        loaded.GitDir(),
		Runner:      service.CommandRunner{},
		ForceLock:   force,
		Output:      out,
		ErrorOutput: errOut,
	}
	if err := m.Clone(ctx, args[0], args[1], commit); err != nil {
		fmt.Fprintf(errOut, "git clone: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "cloned %s through cache\n", args[0])
	return 0
}

func ociCommand(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) == 0 || asksForHelp(args) || (args[0] == "warm" && asksForHelp(args[1:])) {
		ociWarmUsage(out)
		return 0
	}
	if args[0] != "warm" {
		fmt.Fprintln(errOut, "unknown oci command; use: swe-cache oci warm [--root PATH] IMAGE...")
		return 2
	}
	args = args[1:]
	root := ""
	if len(args) >= 2 && args[0] == "--root" {
		root, args = args[1], args[2:]
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "--") {
		fmt.Fprintln(errOut, "usage: swe-cache oci warm [--root PATH] IMAGE...")
		return 2
	}
	c, err := loadConfig(root)
	if err != nil {
		fmt.Fprintf(errOut, "load configuration: %v\n", err)
		return 1
	}
	outputs, err := (service.Manager{Config: c}).Warm(ctx, args)
	for _, output := range outputs {
		fmt.Fprint(out, output)
	}
	if err != nil {
		fmt.Fprintf(errOut, "warm OCI cache: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "warmed %d OCI image(s) through Docker\n", len(args))
	return 0
}

func lifecycle(command string, args []string, out, errOut io.Writer) int {
	options, ok := lifecycleOptions(args, errOut)
	if !ok {
		return 2
	}
	loaded, err := loadConfig(options.root)
	if err != nil {
		fmt.Fprintf(errOut, "load configuration: %v\n", err)
		return 1
	}
	if options.aptPortSet {
		loaded.APT.Port = options.aptPort
	}
	if options.ociPortSet {
		loaded.OCI.Port = options.ociPort
	}
	if options.npmPortSet {
		loaded.NPM.Port = options.npmPort
	}
	if options.goPortSet {
		loaded.Go.Port = options.goPort
	}
	if command != "stop" && (options.aptPortSet || options.ociPortSet || options.npmPortSet || options.goPortSet) {
		if err := loaded.Save(); err != nil {
			fmt.Fprintf(errOut, "save configuration: %v\n", err)
			return 1
		}
	}
	m := service.Manager{Config: loaded}
	ctx := context.Background()
	switch command {
	case "start":
		err = m.Start(ctx)
	case "stop":
		err = m.Stop(ctx)
	case "restart":
		err = m.Restart(ctx)
	}
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", command, err)
		return 1
	}
	fmt.Fprintf(out, "%s complete\n", command)
	return 0
}

type lifecycleFlags struct {
	root                                          string
	aptPort, ociPort, npmPort, goPort             int
	aptPortSet, ociPortSet, npmPortSet, goPortSet bool
}

func lifecycleOptions(args []string, errOut io.Writer) (lifecycleFlags, bool) {
	var result lifecycleFlags
	for len(args) > 0 {
		if len(args) < 2 {
			fmt.Fprintf(errOut, "%s requires a value\n", args[0])
			return result, false
		}
		value := args[1]
		switch args[0] {
		case "--root":
			result.root = value
		case "--apt-port":
			port, err := strconv.Atoi(value)
			if err != nil {
				fmt.Fprintf(errOut, "invalid APT port %q\n", value)
				return result, false
			}
			result.aptPort = port
			result.aptPortSet = true
		case "--oci-port":
			port, err := strconv.Atoi(value)
			if err != nil {
				fmt.Fprintf(errOut, "invalid OCI port %q\n", value)
				return result, false
			}
			result.ociPort = port
			result.ociPortSet = true
		case "--npm-port":
			port, err := strconv.Atoi(value)
			if err != nil {
				fmt.Fprintf(errOut, "invalid npm port %q\n", value)
				return result, false
			}
			result.npmPort = port
			result.npmPortSet = true
		case "--go-port":
			port, err := strconv.Atoi(value)
			if err != nil {
				fmt.Fprintf(errOut, "invalid Go port %q\n", value)
				return result, false
			}
			result.goPort = port
			result.goPortSet = true
		default:
			fmt.Fprintf(errOut, "unknown option %q\n", args[0])
			return result, false
		}
		args = args[2:]
	}
	return result, true
}

func loadConfig(root string) (config.Config, error) {
	c := config.Defaults()
	if root != "" {
		c.Root = root
	}
	return config.Load(c.Path())
}

func rootOption(args []string, errOut io.Writer) (string, bool) {
	if len(args) == 0 {
		return "", true
	}
	if len(args) == 2 && args[0] == "--root" {
		return args[1], true
	}
	fmt.Fprintln(errOut, "expected only optional --root PATH")
	return "", false
}

func initConfig(args []string, out, errOut io.Writer) int {
	c := config.Defaults()
	for len(args) > 0 {
		switch args[0] {
		case "--root":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--root requires a path")
				return 2
			}
			c.Root = args[1]
			args = args[2:]
		case "--image":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--image requires an image reference")
				return 2
			}
			c.Image = args[1]
			args = args[2:]
		case "--apt-port":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--apt-port requires a port")
				return 2
			}
			port, err := strconv.Atoi(args[1])
			if err != nil {
				fmt.Fprintf(errOut, "invalid APT port %q\n", args[1])
				return 2
			}
			c.APT.Port = port
			args = args[2:]
		case "--oci-port":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--oci-port requires a port")
				return 2
			}
			port, err := strconv.Atoi(args[1])
			if err != nil {
				fmt.Fprintf(errOut, "invalid OCI port %q\n", args[1])
				return 2
			}
			c.OCI.Port = port
			args = args[2:]
		case "--npm-port":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--npm-port requires a port")
				return 2
			}
			port, err := strconv.Atoi(args[1])
			if err != nil {
				fmt.Fprintf(errOut, "invalid npm port %q\n", args[1])
				return 2
			}
			c.NPM.Port = port
			args = args[2:]
		case "--go-port":
			if len(args) < 2 {
				fmt.Fprintln(errOut, "--go-port requires a port")
				return 2
			}
			port, err := strconv.Atoi(args[1])
			if err != nil {
				fmt.Fprintf(errOut, "invalid Go port %q\n", args[1])
				return 2
			}
			c.Go.Port = port
			args = args[2:]
		default:
			fmt.Fprintf(errOut, "unknown init option %q\n", args[0])
			return 2
		}
	}
	if err := c.Save(); err != nil {
		fmt.Fprintf(errOut, "initialize cache: %v\n", err)
		return 1
	}
	if err := (service.Manager{Config: c}).EnsureImage(context.Background()); err != nil {
		fmt.Fprintf(errOut, "initialize service image: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "initialized persistent cache at %s\n", c.Root)
	return 0
}

func isKnown(command string) bool {
	return strings.Contains(" start stop restart status doctor stats gc git oci ", " "+command+" ")
}

func usage(out io.Writer) {
	fmt.Fprint(out, `Usage: swe-cache <command> [options]

Global options:
  -h, --help                          show this help or command-specific help
  --readme                            print the embedded README
  --version                           print the binary version

Commands:
  init [--root PATH] [--image REF] [--apt-port PORT] [--oci-port PORT] [--npm-port PORT] [--go-port PORT]
                                      create persistent layout and config
  start | restart [--root PATH] [--apt-port PORT] [--oci-port PORT] [--npm-port PORT] [--go-port PORT]
                                      manage the disposable service container
  stop [--root PATH]                  stop the service container
  status | doctor | stats            inspect cache health and use
  gc [apt|oci|git|npm|go|--all]      perform safe maintenance
  git clone [--commit SHA] URL DIR   clone through a host-side bare mirror
  oci warm IMAGE...                  prewarm Zot through Docker pulls

Examples:
  swe-cache init --root /srv/swe-cache --oci-port 5500
  swe-cache start
  swe-cache status
  swe-cache git clone --commit 0123abcd https://github.com/acme/project.git ./project

Run "swe-cache <command> --help" for command options and examples.
`)
}

func asksForHelp(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
}

func commandUsage(args []string, out, errOut io.Writer) int {
	switch strings.Join(args, " ") {
	case "init":
		initUsage(out)
	case "start", "stop", "restart":
		lifecycleUsage(args[0], out)
	case "status", "doctor", "stats":
		inspectUsage(args[0], out)
	case "gc":
		gcUsage(out)
	case "git", "git clone":
		gitCloneUsage(out)
	case "oci", "oci warm":
		ociWarmUsage(out)
	default:
		fmt.Fprintf(errOut, "unknown command for help: %s\n", strings.Join(args, " "))
		usage(errOut)
		return 2
	}
	return 0
}

func initUsage(out io.Writer) {
	fmt.Fprint(out, `Usage: swe-cache init [--root PATH] [--image REF] [--apt-port PORT] [--oci-port PORT] [--npm-port PORT] [--go-port PORT]

Creates the persistent cache layout and configuration. If the selected service
image is not local, init builds it from the build context embedded in this binary.

Examples:
  swe-cache init
  swe-cache init --root /srv/swe-cache --apt-port 3142 --oci-port 5500 --npm-port 4873 --go-port 3000
  swe-cache init --image registry.example/swe-cache-services:0.3.0
`)
}

func lifecycleUsage(command string, out io.Writer) {
	if command == "stop" {
		fmt.Fprint(out, `Usage: swe-cache stop [--root PATH]

Stops the disposable service container without deleting persistent cache data.

Example:
  swe-cache stop --root /srv/swe-cache
`)
		return
	}
	fmt.Fprintf(out, `Usage: swe-cache %s [--root PATH] [--apt-port PORT] [--oci-port PORT] [--npm-port PORT] [--go-port PORT]

Starts or recreates the disposable service container. Explicit port flags are
saved to the cache configuration for later starts.

Examples:
  swe-cache %s
  swe-cache %s --apt-port 3142 --oci-port 5510 --npm-port 4873 --go-port 3000
`, command, command, command)
}

func inspectUsage(command string, out io.Writer) {
	fmt.Fprintf(out, `Usage: swe-cache %s [--root PATH]

Examples:
  swe-cache %s
  swe-cache %s --root /srv/swe-cache
`, command, command, command)
}

func gcUsage(out io.Writer) {
	fmt.Fprint(out, `Usage: swe-cache gc [--root PATH] {apt|oci|git|npm|go|--all}

Git maintenance repacks mirror objects without pruning them. APT, OCI, npm,
and Go cache retention is managed by their services and is not deleted here.

Examples:
  swe-cache gc git
  swe-cache gc --root /srv/swe-cache --all
`)
}

func gitCloneUsage(out io.Writer) {
	fmt.Fprint(out, `Usage: swe-cache git clone [--root PATH] [--commit SHA] [--force] URL DESTINATION

Creates or reuses a host-side bare mirror, then clones from that mirror. The
destination receives the original URL as its origin. With --commit, swe-cache
checks the mirror first and contacts the upstream only if that commit is absent.
Git clone progress is streamed while the mirror and destination are created.

Options:
  --root PATH                         cache root containing Git mirrors
  --commit SHA                        require and check out a specific commit
  --force                             remove one existing stale repository lock

Use --force only after confirming that no clone for the repository is active.

Examples:
  swe-cache git clone https://github.com/acme/project.git ./project
  swe-cache git clone --commit 0123abcd https://github.com/acme/project.git ./project
  swe-cache git clone --force https://github.com/acme/project.git ./project
  swe-cache git clone --root /srv/swe-cache git@github.com:acme/project.git ./project
`)
}

func ociWarmUsage(out io.Writer) {
	fmt.Fprint(out, `Usage: swe-cache oci warm [--root PATH] IMAGE...

Pre-pulls one or more images after starting the cache service. Docker must be
configured to use Zot as its Docker Hub registry mirror for these pulls to
populate the durable Zot cache. Use this before an evaluator run so cold Zot
sync work does not block BuildKit metadata resolution.

Examples:
  swe-cache oci warm node:24-bookworm docker/dockerfile:1.7
  swe-cache oci warm --root /srv/swe-cache python:3.13-bookworm
`)
}
