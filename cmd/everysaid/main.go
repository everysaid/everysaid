// Ports everysaid/cli.py and the scripts the app runs: one binary, with subcommands.
//
//	everysaid import [--db PATH] [IMPORTER...]   fill the archive (the importers, in their order)
//	everysaid serve [--host H] [--port P]        the app: API, UI, plugins, live connections
//	everysaid mcp [--db PATH]                    the MCP server for an assistant (stdio)
//	everysaid demo [--dir DIR] [--serve]         a demo archive of invented people, for trying the app
//	everysaid user ...                           users, passkeys, recovery
//	everysaid ferdium install|zip                Everysaid as a service of Ferdium (or Franz)
//	everysaid iphone-sync | iphone-ls | iphone-verify | android-export | telegram-sync
//	                                             the extraction the sources run, by hand
//
// `everysaid sms calls ...` (importer names alone) still runs the importers.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"everysaid/internal/all"
	"everysaid/internal/android"
	"everysaid/internal/archive"
	"everysaid/internal/demo"
	"everysaid/internal/ferdium"
	"everysaid/internal/i18n"
	"everysaid/internal/importers"
	"everysaid/internal/iphone"
	"everysaid/internal/mcp"
	"everysaid/internal/telegram"
	"everysaid/internal/viber"
)

var _ = all.Loaded

// commands are the subcommands; serve and user are added by the server's file.
var commands = map[string]func(args []string) error{
	"import":      cmdImport,
	"mcp":         mcp.Main,
	"demo":        demo.Main,
	"iphone-sync": func(a []string) error { return iphone.SyncMain(a, os.Stdout) },
	"iphone-ls":   func(a []string) error { return iphone.LsMain(a, os.Stdout) },
	"iphone-verify": func(a []string) error {
		return iphone.VerifyMain(a, os.Stdout)
	},
	"android-export": func(a []string) error { return android.ExportMain(a, os.Stdout) },
	"telegram-sync":  func(a []string) error { return telegram.SyncMain(a, os.Stdout) },
	"viber":          func(a []string) error { return viber.Main(a, os.Stdout) },
	"ferdium":        func(a []string) error { return ferdium.Main(a, os.Stdout) },
}

const usage = `everysaid: a personal archive of messages and calls.

  everysaid import [--db PATH] [IMPORTER...]   fill the archive (the importers, in their order)
  everysaid serve [--host H] [--port P]        the app: API, UI, plugins, live connections
  everysaid mcp [--db PATH]                    the MCP server for an assistant (stdio)
  everysaid demo [--dir DIR] [--serve]         a demo archive of invented people, for trying the app
  everysaid user ...                           users, passkeys, recovery (everysaid user -h)
  everysaid viber start|stop|restart|status    Viber Desktop, as the Viber Desktop source keeps it
  everysaid ferdium install|zip                Everysaid as a service of Ferdium (or Franz)
  everysaid iphone-sync | iphone-ls | iphone-verify | android-export | telegram-sync
                                               the extraction the sources run, by hand
`

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("everysaid import", flag.ContinueOnError)
	dbPath := fs.String("db", archive.DB(), "archive database")
	fs.Usage = func() {
		var names []string
		for _, imp := range importers.Names {
			names = append(names, imp.Name)
		}
		fmt.Fprintf(fs.Output(), "%s\n", i18n.Say("Fill the archive from the sources. Importers: {names}; default: config [import] importers, else all.",
			map[string]any{"names": strings.Join(names, ", ")}))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	chosen := importers.Chosen(fs.Args())
	var unknown []string
	for _, n := range chosen {
		if !importers.Known(n) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return errors.New(i18n.Say("unknown importers: {names}", map[string]any{"names": strings.Join(unknown, ", ")}))
	}
	a, err := archive.Open(*dbPath)
	if err != nil {
		return err
	}
	defer a.Close()
	return importers.Run(a, chosen, func(line string) { fmt.Println(line) })
}

func run(args []string, stderr io.Writer) int {
	umask()
	if len(args) > 0 {
		if f, ok := commands[args[0]]; ok {
			if err := f(args[1:]); err != nil {
				if errors.Is(err, flag.ErrHelp) {
					return 0
				}
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
	}
	if err := cmdImport(args); err != nil { // the importers by name, as before
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }
