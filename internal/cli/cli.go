package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
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
	if isKnown(args[0]) {
		fmt.Fprintf(errOut, "%s: not implemented yet\n", args[0])
		return 3
	}
	fmt.Fprintf(errOut, "unknown command %q\n", args[0])
	usage(errOut)
	return 2
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
