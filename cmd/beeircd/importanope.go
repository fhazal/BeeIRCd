package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/fhazal/beeircd/internal/anopedb"
	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/store"
)

// importRun lets tests answer the question and skip the terminal check.
type importRun struct {
	in  *bufio.Reader
	tty bool
}

func cmdImportAnope(args []string, stdout, stderr io.Writer) int {
	return importAnope(args, stdout, stderr, importRun{in: bufio.NewReader(os.Stdin), tty: term.IsTerminal(int(os.Stdin.Fd()))})
}

// importAnope reads an Anope 2.0 anope.db, shows what would come in (by
// importing into a copy), and after a yes imports into services.db.
func importAnope(args []string, stdout, stderr io.Writer, r importRun) int {
	fs := flag.NewFlagSet("import-anope", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultConfig, "config file")
	yes := fs.Bool("yes", false, "don't ask before importing (for scripts)")
	replace := fs.Bool("replace", false, "names that BeeIRCd has already: take Anope's instead")
	dry := fs.Bool("dry-run", false, "only show what would come in")
	unrealOpers := fs.String("unreal-opers", "", "also bring the oper blocks of this unrealircd.conf over (after the accounts)")
	// Flags may come before or after the file name.
	var files []string
	for {
		if err := fs.Parse(args); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(files) != 1 {
		fmt.Fprintln(stderr, "Usage: beeircd import-anope [-dry-run] [-yes] [-replace] [-unreal-opers unrealircd.conf] [-config file] <anope.db>")
		return 2
	}
	p := colourFor(stdout)
	c := &checker{w: stdout, p: p}
	src := files[0]

	cfg, err := config.Load(*path)
	if err != nil {
		c.bad("config: " + err.Error())
		return 1
	}
	if !cfg.Services.On() {
		c.bad("this server's services are off: import on the server that runs them (the hub)")
		return 1
	}
	f, err := os.Open(src)
	if err != nil {
		c.bad(err.Error())
		return 1
	}
	fi, _ := f.Stat()
	objs, err := anopedb.Parse(f)
	f.Close()
	if err != nil {
		c.bad(src + ": " + err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "\n  %-19s %s  (%d records, written %s)\n", "Anope 2.0 database", src, len(objs), fi.ModTime().Format("2 Jan 2006 15:04"))

	_, askErr := ask(cfg, control.CmdStatus, "", 3*time.Second)
	running := !errors.Is(askErr, control.ErrNotRunning)
	db := cfg.Services.Database
	state := p.ok("stopped ✓")
	if running {
		state = p.bad("running")
	}
	dbSize := "new"
	if st, err := os.Stat(db); err == nil {
		dbSize = size(st.Size())
	}
	fmt.Fprintf(stdout, "  %-19s %s   %s %s\n\n", "BeeIRCd", state, filepath.Base(db), dbSize)

	// The report comes from a real import into a copy, so it is exactly
	// what the import will do.
	rep, err := importInto(db, objs, anopedb.Options{Replace: *replace}, *unrealOpers, true)
	if err != nil {
		c.bad("reading: " + err.Error())
		return 1
	}
	printReport(stdout, p, rep, *replace)
	if *dry {
		fmt.Fprintln(stdout, "  Dry run: nothing was written.")
		return 0
	}
	if running {
		c.bad("BeeIRCd is running: stop it first (beeircd stop), then run this again")
		return 1
	}
	if !*yes {
		if !r.tty {
			c.bad("no terminal to ask on: add -yes")
			return 1
		}
		fmt.Fprint(stdout, "  Import? [y/N] ")
		line, _ := r.in.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "  Nothing changed.")
			return 0
		}
	}

	now := time.Now()
	b, err := offlineBackup(cfg, "", now)
	if err != nil {
		c.bad("backup first: " + err.Error())
		return 1
	}
	fmt.Fprintf(stdout, "  Backup first: %s  ·  %s\n", filepath.Base(b.File), filepath.Dir(b.File))
	undo := "rm " + db // no database before: the import made it
	if _, err := os.Stat(db); err == nil {
		before := db + ".before-anope-" + now.Format("20060102-150405")
		if err := copyDB(db, before); err != nil {
			c.bad("copy of " + db + ": " + err.Error())
			return 1
		}
		undo = "cp " + before + " " + db
	}
	if _, err := importInto(db, objs, anopedb.Options{Replace: *replace}, *unrealOpers, false); err != nil {
		c.bad("import: " + err.Error())
		fmt.Fprintf(stdout, "  Put it back:  %s\n", undo)
		return 1
	}
	c.ok(fmt.Sprintf("imported in %s · nothing else changed", time.Since(now).Round(100*time.Millisecond)))
	fmt.Fprintf(stdout, "  Undo (while stopped):  %s\n", undo)
	fmt.Fprintf(stdout, "  Start BeeIRCd:  beeircd start\n\n")
	return 0
}

// importInto imports into db, or into a throwaway copy of it (trial).
func importInto(db string, objs []anopedb.Object, opt anopedb.Options, unrealConf string, trial bool) (*anopedb.Report, error) {
	target := db
	if trial {
		dir, err := os.MkdirTemp("", "beeircd-import-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		target = filepath.Join(dir, "services.db")
		if _, err := os.Stat(db); err == nil {
			src, err := store.Open(db)
			if err != nil {
				return nil, err
			}
			err = src.BackupTo(target)
			src.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	var owner os.FileInfo
	if fi, err := os.Stat(db); err == nil {
		owner = fi
	} else if fi, err := os.Stat(filepath.Dir(db)); err == nil {
		owner = fi
	}
	st, err := store.Open(target)
	if err != nil {
		return nil, err
	}
	rep, err := anopedb.Import(st, objs, opt)
	if err == nil && unrealConf != "" {
		var f *os.File
		if f, err = os.Open(unrealConf); err == nil {
			err = anopedb.ImportUnrealOpers(st, []io.Reader{f}, opt, rep)
			f.Close()
		}
	}
	if cerr := st.Close(); err == nil {
		err = cerr
	}
	if !trial && owner != nil {
		// Run as root, the files must stay the server's.
		for _, f := range []string{target, target + "-wal", target + "-shm"} {
			if _, serr := os.Stat(f); serr == nil {
				copyOwner(owner, f)
			}
		}
	}
	return rep, err
}

// copyDB copies the database (with what its write-ahead log holds) to dst.
func copyDB(db, dst string) error {
	st, err := store.Open(db)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.BackupTo(dst)
}

func printReport(w io.Writer, p paint, r *anopedb.Report, replace bool) {
	fmt.Fprintf(w, "  %s\n", p.title("Comes in"))
	if len(r.In) == 0 {
		fmt.Fprintln(w, "  "+p.dim("· nothing"))
	}
	for _, n := range r.In {
		fmt.Fprintf(w, "  %s %d %s\n", p.ok("✓"), n.N, n.What)
	}
	section := func(title, mark string, lines []string, colour func(string) string) {
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(w, "\n  %s\n", colour(title))
		for _, l := range lines {
			fmt.Fprintf(w, "  %s %s\n", colour(mark), l)
		}
	}
	section("Changed on the way", "!", r.Changed, p.warn)
	section("Already in BeeIRCd", "!", r.Clashes, p.warn)
	if len(r.Clashes) > 0 && !replace {
		fmt.Fprintln(w, "    "+p.dim("use -replace to take Anope's instead"))
	}
	var unknown []string
	for _, u := range r.Unknown {
		unknown = append(unknown, fmt.Sprintf("%d %s record(s): not something Anope 2.0 core writes", u.N, u.What))
	}
	section("Skipped", "·", append(r.Skipped, unknown...), p.dim)
	fmt.Fprintln(w)
}
