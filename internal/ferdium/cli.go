package ferdium

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"everysaid/internal/config"
	"everysaid/internal/i18n"
)

// Main is `everysaid ferdium install|zip`: the recipe into the folders of development recipes of
// the apps found here (or one given), or as a zip to carry to another machine.
func Main(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("ferdium", flag.ContinueOnError)
	url := fs.String("url", config.ServerOrigin, i18n.Say("the app's address, as the devices use it", nil))
	dir := fs.String("dir", "", i18n.Say("a folder of development recipes (default: those of Ferdium, Ferdi and Franz found here)", nil))
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "everysaid ferdium install [--url URL] [--dir DIR] | zip [--url URL] FILE\n\n"+
			i18n.Say("Everysaid as a service of Ferdium (or Franz), with the unread chats on its icon: installed here, or as a zip.", nil))
		fs.PrintDefaults()
	}
	var verb string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	say := func(text string, params map[string]any) { fmt.Fprintln(out, i18n.Say(text, params)) }
	switch verb {
	case "install":
		devs := DevFolders()
		if *dir != "" {
			devs = []string{*dir}
		}
		if len(devs) == 0 {
			return errors.New(i18n.Say("no Ferdium, Ferdi or Franz here: --dir their folder of development recipes, or a zip (everysaid ferdium zip)", nil))
		}
		for _, d := range devs {
			at, err := Install(d, *url)
			if err != nil {
				return err
			}
			say("installed: {dir}", map[string]any{"dir": at})
		}
		say("In the app: restart it, then add the service Everysaid (under Development).", nil)
	case "zip":
		if fs.NArg() != 1 {
			fs.Usage()
			return flag.ErrHelp
		}
		f, err := os.Create(fs.Arg(0))
		if err != nil {
			return err
		}
		if err := Zip(f, *url); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		say("written: {file} (unpack it into the app's recipes/dev folder)", map[string]any{"file": fs.Arg(0)})
	default:
		fs.Usage()
		return flag.ErrHelp
	}
	return nil
}
