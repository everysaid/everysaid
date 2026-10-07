package phones

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"everysaid/internal/i18n"
)

// Args is the little of Python's argparse the scripts use: options with a value (-o DIR, --out
// DIR, --out=DIR, -oDIR), switches, a list (nargs="+"), an int, and positionals (one optional at
// the end). -h/--help prints the usage.
type Args struct {
	Params            map[string]any // put into the {places} of the helps
	prog, description string
	opts              []*opt
	pos               []*positional
}

type opt struct {
	names   []string
	metavar string
	help    string
	str     *string
	flag    *bool
	list    *[]string
	num     *int
}

type positional struct {
	name, help string
	optional   bool
	value      *string
	set        bool
}

func NewArgs(prog, description string) *Args { return &Args{prog: prog, description: description} }

func (a *Args) add(names, metavar, help string) *opt {
	o := &opt{names: strings.Split(names, ","), metavar: metavar, help: help}
	a.opts = append(a.opts, o)
	return o
}

// String is an option with a value; names as "-o,--out".
func (a *Args) String(names, metavar, def, help string) *string {
	v := def
	a.add(names, metavar, help).str = &v
	return &v
}

func (a *Args) Bool(names, help string) *bool {
	v := false
	a.add(names, "", help).flag = &v
	return &v
}

func (a *Args) List(names, metavar, help string) *[]string {
	var v []string
	a.add(names, metavar, help).list = &v
	return &v
}

func (a *Args) Int(names, metavar string, def int, help string) *int {
	v := def
	a.add(names, metavar, help).num = &v
	return &v
}

// Pos is a positional argument; optional ones (nargs="?") come last.
func (a *Args) Pos(name, def, help string, optional bool) *string {
	v := def
	a.pos = append(a.pos, &positional{name: name, help: help, optional: optional, value: &v})
	return &v
}

func (a *Args) usage() string {
	parts := []string{"usage: " + a.prog, "[-h]"}
	for _, o := range a.opts {
		s := o.names[0]
		switch {
		case o.list != nil:
			s += " " + o.metavar + " [" + o.metavar + " ...]"
		case o.flag == nil:
			s += " " + o.metavar
		}
		parts = append(parts, "["+s+"]")
	}
	for _, p := range a.pos {
		if p.optional {
			parts = append(parts, "["+p.name+"]")
		} else {
			parts = append(parts, p.name)
		}
	}
	return strings.Join(parts, " ")
}

func (a *Args) help(out io.Writer) {
	fmt.Fprintln(out, a.usage())
	fmt.Fprintln(out)
	fmt.Fprintln(out, i18n.Say(a.description, nil))
	if len(a.pos) > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, i18n.Say("positional arguments:", nil))
		for _, p := range a.pos {
			fmt.Fprintf(out, "  %-22s %s\n", p.name, i18n.Say(p.help, a.Params))
		}
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, i18n.Say("options:", nil))
	fmt.Fprintf(out, "  %-22s %s\n", "-h, --help", i18n.Say("show this help message and exit", nil))
	for _, o := range a.opts {
		s := strings.Join(o.names, ", ")
		if o.flag == nil {
			s += " " + o.metavar
		}
		fmt.Fprintf(out, "  %-22s %s\n", s, i18n.Say(o.help, a.Params))
	}
}

// fail is argparse's error: the usage and the message on stderr, exit code 2.
func (a *Args) fail(msg string) error {
	return &Failure{Text: a.usage() + "\n" + a.prog + ": error: " + msg, Code: 2}
}

func (a *Args) find(name string) *opt {
	for _, o := range a.opts {
		for _, n := range o.names {
			if n == name {
				return o
			}
		}
	}
	return nil
}

// Parse reads the arguments; -h prints the help into out and ends with code 0.
func (a *Args) Parse(args []string, out io.Writer) error {
	posi := 0
	onlyPos := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if onlyPos || arg == "-" || !strings.HasPrefix(arg, "-") || isNumber(arg) {
			if posi >= len(a.pos) {
				return a.fail("unrecognized arguments: " + strings.Join(args[i:], " "))
			}
			*a.pos[posi].value = arg
			a.pos[posi].set = true
			posi++
			continue
		}
		if arg == "--" {
			onlyPos = true
			continue
		}
		if arg == "-h" || arg == "--help" {
			a.help(out)
			return &Failure{Code: 0}
		}
		name, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			name, value, hasValue = strings.Cut(arg, "=")
		} else if len(arg) > 2 {
			name, value, hasValue = arg[:2], arg[2:], true
		}
		o := a.find(name)
		if o == nil {
			return a.fail("unrecognized arguments: " + arg)
		}
		if o.flag != nil {
			if hasValue {
				return a.fail("argument " + strings.Join(o.names, "/") + ": ignored explicit argument '" + value + "'")
			}
			*o.flag = true
			continue
		}
		var values []string
		if hasValue {
			values = append(values, value)
		}
		for (o.list != nil || len(values) == 0) && i+1 < len(args) && (!strings.HasPrefix(args[i+1], "-") || isNumber(args[i+1])) {
			i++
			values = append(values, args[i])
		}
		if len(values) == 0 {
			if o.list != nil {
				return a.fail("argument " + strings.Join(o.names, "/") + ": expected at least one argument")
			}
			return a.fail("argument " + strings.Join(o.names, "/") + ": expected one argument")
		}
		switch {
		case o.list != nil:
			*o.list = values
		case o.num != nil:
			n, err := strconv.Atoi(values[0])
			if err != nil {
				return a.fail("argument " + strings.Join(o.names, "/") + ": invalid int value: '" + values[0] + "'")
			}
			*o.num = n
		default:
			*o.str = values[0]
		}
	}
	var missing []string
	for _, p := range a.pos {
		if !p.set && !p.optional {
			missing = append(missing, p.name)
		}
	}
	if len(missing) > 0 {
		return a.fail("the following arguments are required: " + strings.Join(missing, ", "))
	}
	return nil
}

func isNumber(s string) bool {
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}
