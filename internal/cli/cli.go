package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
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
	if isKnown(args[0]) {
		fmt.Fprintf(errOut, "%s: not implemented yet\n", args[0])
		return 3
	}
	fmt.Fprintf(errOut, "unknown command %q\n", args[0])
	usage(errOut)
	return 2
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
	c := config.Defaults()
	if root != "" {
		c.Root = root
	}
	loaded, err := config.Load(c.Path())
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
	root, ok := rootOption(args, errOut)
	if !ok {
		return 2
	}
	c := config.Defaults()
	if root != "" {
		c.Root = root
	}
	loaded, err := config.Load(c.Path())
	if err != nil {
		fmt.Fprintf(errOut, "load configuration: %v\n", err)
		return 1
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
  init [--root PATH] [--image REF]  create persistent layout and config
  start | stop | restart            manage the disposable service container
  status | doctor | stats            inspect cache health and use
  gc [apt|oci|git|--all]             perform safe maintenance
  git clone [--commit SHA] URL DIR   clone through a host-side bare mirror
  version                            print the binary version
`)
}
