package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/luamod"
)

const moduleUsage = `Usage:
  beeircd module new <name>        write a starter module <name>.lua into the modules folder
  beeircd module check <file.lua>  look for mistakes in a module without a server
  beeircd module hooks             list the hooks a module can use

Options for "new":
  -config FILE   the beeircd.conf whose modules folder is used (default /etc/beeircd/beeircd.conf)
  -dir FOLDER    write into this folder instead
`

// cmdModule is `beeircd module ...`: tools for people who write modules.
func cmdModule(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, moduleUsage)
		return 0
	}
	switch strings.ToLower(args[0]) {
	case "new":
		return moduleNew(args[1:], stdout, stderr)
	case "check":
		return moduleCheck(args[1:], stdout, stderr)
	case "hooks":
		return moduleHooks(stdout)
	}
	fmt.Fprintf(stderr, "Unknown: beeircd module %s\n\n%s", args[0], moduleUsage)
	return 2
}

func moduleNew(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("module new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "/etc/beeircd/beeircd.conf", "config file")
	dir := fs.String("dir", "", "folder to write into")
	// The name may come before or after the options.
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if name == "" {
		name = fs.Arg(0)
	}
	name = strings.TrimSuffix(name, ".lua")
	if !luamod.ValidName(name) {
		fmt.Fprintf(stderr, "%q can't be a module name: use small letters, digits, - and _, starting with a letter.\n", name)
		return 2
	}
	folder := *dir
	if folder == "" {
		folder = filepath.Join(filepath.Dir(*cfgPath), "modules")
		if cfg, err := config.Load(*cfgPath); err == nil {
			folder = cfg.Modules.DirOr(*cfgPath)
		}
	}
	if err := os.MkdirAll(folder, 0o755); err != nil {
		fmt.Fprintf(stderr, "Can't make the folder %s: %v\n", folder, err)
		return 1
	}
	path := filepath.Join(folder, name+".lua")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			fmt.Fprintf(stderr, "%s is there already; it was not touched.\n", path)
		} else {
			fmt.Fprintf(stderr, "Can't write %s: %v\n", path, err)
		}
		return 1
	}
	_, err = f.WriteString(luamod.Starter(name))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Fprintf(stderr, "Can't write %s: %v\n", path, err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %s\n\nNext:\n  1. Change it to do what you want.\n  2. beeircd module check %s\n  3. On IRC as an oper: /MODULE LOAD %s\n  4. To keep it after a restart, add \"%s\" to [modules] load in beeircd.conf.\n", path, path, name, name)
	return 0
}

func moduleCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, "Give the file to check: beeircd module check greet.lua\n")
		return 2
	}
	bad := 0
	for _, path := range args {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(stderr, "Can't read %s: %v\n", path, err)
			bad++
			continue
		}
		name := strings.TrimSuffix(filepath.Base(path), ".lua")
		problems := luamod.CheckSource(name, string(src))
		if len(problems) == 0 {
			fmt.Fprintf(stdout, "%s: no mistakes found\n", path)
			continue
		}
		bad++
		fmt.Fprintf(stdout, "%s: %d thing(s) to fix\n", path, len(problems))
		for _, p := range problems {
			fmt.Fprintf(stdout, "  - %s\n", p)
		}
	}
	if bad > 0 {
		return 1
	}
	return 0
}

func moduleHooks(stdout io.Writer) int {
	var now, later []string
	for name, h := range luamod.Hooks {
		kind := "told after"
		if h.Kind == luamod.Check {
			kind = "asked before"
		}
		line := fmt.Sprintf("  %-18s %-13s %s", name, kind, h.About)
		if h.Built {
			now = append(now, line)
		} else {
			later = append(later, line)
		}
	}
	sort.Strings(now)
	sort.Strings(later)
	fmt.Fprintf(stdout, "Hooks in this version (module interface %d):\n%s\n\nPlanned, not in this version yet:\n%s\n", luamod.API, strings.Join(now, "\n"), strings.Join(later, "\n"))
	return 0
}
