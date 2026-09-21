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

const Version = "0.1.0"

func Run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage(out)
		return 0
	}
	if args[0] == "version" || args[0] == "--version" {
		fmt.Fprintf(out, "swe-cache %s\n", Version)
		return 0
	}
	if args[0] == "init" {
		return initConfig(args[1:], out, errOut)
	}
	if args[0] == "start" || args[0] == "stop" || args[0] == "restart" {
		return lifecycle(args[0], args[1:], out, errOut)
	}
	if args[0] == "git" {
		return gitCommand(args[1:], out, errOut)
	}
	if args[0] == "status" || args[0] == "doctor" || args[0] == "stats" {
		return inspectCommand(args[0], args[1:], out, errOut)
	}
	if args[0] == "gc" {
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
		fmt.Fprintf(out, "docker: %s\ngit: %s\nservice container: %s\napt-cacher-ng: %s\nzot: %s\n", r.Docker, r.Git, r.Container, r.APT, r.OCI)
		for _, name := range []string{"apt", "oci", "git"} {
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
		fmt.Fprintf(out, "apt bytes: %d\noci bytes: %d\ngit bytes: %d\ngit mirrors: %d\n", stats.APTBytes, stats.OCIBytes, stats.GitBytes, stats.GitMirrors)
	}
	return 0
}

func gcCommand(args []string, out, errOut io.Writer) int {
	root := ""
	if len(args) >= 2 && args[0] == "--root" {
		root, args = args[1], args[2:]
	}
	if len(args) != 1 || (args[0] != "git" && args[0] != "apt" && args[0] != "oci" && args[0] != "--all") {
		fmt.Fprintln(errOut, "usage: swe-cache gc [--root PATH] {apt|oci|git|--all}")
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
	if args[0] == "apt" || args[0] == "oci" || args[0] == "--all" {
		fmt.Fprintln(out, "APT and OCI retention are service-managed; no cache entries were deleted")
	}
	return 0
}

func gitCommand(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] != "clone" {
		fmt.Fprintln(errOut, "usage: swe-cache git clone [--root PATH] [--commit SHA] URL DESTINATION")
		return 2
	}
	args = args[1:]
	root, commit := "", ""
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		if len(args) < 2 {
			fmt.Fprintf(errOut, "%s requires a value\n", args[0])
			return 2
		}
		switch args[0] {
		case "--root":
			root = args[1]
		case "--commit":
			commit = args[1]
		default:
			fmt.Fprintf(errOut, "unknown git clone option %q\n", args[0])
			return 2
		}
		args = args[2:]
	}
	if len(args) != 2 {
		fmt.Fprintln(errOut, "usage: swe-cache git clone [--root PATH] [--commit SHA] URL DESTINATION")
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
	m := gitcache.Manager{Root: loaded.GitDir(), Runner: service.CommandRunner{}}
	if err := m.Clone(context.Background(), args[0], args[1], commit); err != nil {
		fmt.Fprintf(errOut, "git clone: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "cloned %s through cache\n", args[0])
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
	if command != "stop" && (options.aptPortSet || options.ociPortSet) {
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
	root                   string
	aptPort, ociPort       int
	aptPortSet, ociPortSet bool
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
		default:
			fmt.Fprintf(errOut, "unknown init option %q\n", args[0])
			return 2
		}
	}
	if err := c.Save(); err != nil {
		fmt.Fprintf(errOut, "initialize cache: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "initialized persistent cache at %s\n", c.Root)
	return 0
}

func isKnown(command string) bool {
	return strings.Contains(" start stop restart status doctor stats gc git ", " "+command+" ")
}

func usage(out io.Writer) {
	fmt.Fprint(out, `Usage: swe-cache <command> [options]

Commands:
  init [--root PATH] [--image REF] [--apt-port PORT] [--oci-port PORT]
                                      create persistent layout and config
  start | restart [--root PATH] [--apt-port PORT] [--oci-port PORT]
                                      manage the disposable service container
  stop [--root PATH]                  stop the service container
  status | doctor | stats            inspect cache health and use
  gc [apt|oci|git|--all]             perform safe maintenance
  git clone [--commit SHA] URL DIR   clone through a host-side bare mirror
  version                            print the binary version
`)
}
