package services

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The English lines people can get from the translated services: the
// format strings of their replies (read from the source) and their HELP
// texts (read from the running bots).

// langSkip are files whose lines are for opers, logs or the Hive Panel.
var langSkip = regexp.MustCompile(`^(operserv.*|osoper|defcon|netbans|kline|operdata|link_bans|panel_.*|web.*|api|logchan|spamfilter|core|lang)\.go$`)

// replyFuncs take the format (or text) as their argument at the index.
var replyFuncs = map[string]int{"cs": 1, "ns": 1, "ms": 1, "hs": 1, "he": 1, "bs": 1, "say": 2, "svcReply": 2, "tr": 1, "trFor": 2}

func englishLines(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	consts := map[string]string{}
	var parsed []*ast.File
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, af)
		ast.Inspect(af, func(n ast.Node) bool {
			if vs, ok := n.(*ast.ValueSpec); ok {
				for i, name := range vs.Names {
					if i < len(vs.Values) {
						if v, ok := strValue(vs.Values[i], consts); ok {
							consts[name.Name] = v
						}
					}
				}
			}
			return true
		})
	}
	out := map[string]bool{}
	for i, af := range parsed {
		if langSkip.MatchString(filepath.Base(fset.File(af.Pos()).Name())) {
			continue
		}
		_ = i
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			at, ok := replyFuncs[sel.Sel.Name]
			if !ok || len(call.Args) <= at {
				return true
			}
			if sel.Sel.Name == "svcReply" || sel.Sel.Name == "say" || sel.Sel.Name == "trFor" {
				if sv, ok := call.Args[at-1].(*ast.SelectorExpr); ok && sv.Sel.Name == "operServ" {
					return true
				}
			}
			arg := call.Args[at]
			if inner, ok := arg.(*ast.CallExpr); ok {
				if f, ok := inner.Fun.(*ast.SelectorExpr); ok && f.Sel.Name == "Sprintf" && len(inner.Args) > 0 {
					arg = inner.Args[0]
				}
			}
			if v, ok := strValue(arg, consts); ok && wordy(v) {
				out[v] = true
			}
			return true
		})
	}
	s, _ := newServices(t)
	for _, sv := range s.services {
		if sv.botID != 0 || !s.translated(sv) {
			continue
		}
		if wordy(sv.intro) {
			out[sv.intro] = true
		}
		for _, cmd := range sv.cmds {
			out[cmd.short] = true
			out[cmd.long] = true
		}
		for _, g := range sv.groups {
			out[g.title] = true
		}
	}
	return out
}

// wordy is a line with words in it, not just "%s" or "  %s  %s".
func wordy(s string) bool {
	return regexp.MustCompile(`[A-Za-z]{2,}`).MatchString(regexp.MustCompile(`%(\[\d+\])?[-+# 0-9.*]*[a-zA-Z%]`).ReplaceAllString(s, ""))
}

func strValue(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			s, err := strconv.Unquote(v.Value)
			return s, err == nil
		}
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.BinaryExpr:
		if v.Op == token.ADD {
			a, ok1 := strValue(v.X, consts)
			b, ok2 := strValue(v.Y, consts)
			return a + b, ok1 && ok2
		}
	case *ast.ParenExpr:
		return strValue(v.X, consts)
	}
	return "", false
}

var verbRe = regexp.MustCompile(`%(\[\d+\])?[-+# 0-9.*]*[a-zA-Z%]`)

// verbs are a line's formatting verbs, sorted, so a translation can move
// them but not drop or add one.
func verbs(s string) string {
	v := verbRe.FindAllString(s, -1)
	sort.Strings(v)
	return strings.Join(v, " ")
}

// Every line of a language file is a line services really say, with the
// same %s verbs; and the language has every line (BEE_LANG_TODO=<file>
// writes the missing ones out for translating).
func TestLanguageFiles(t *testing.T) {
	en := englishLines(t)
	if len(en) < 500 {
		t.Fatalf("only %d English lines found: the reader is broken", len(en))
	}
	for code, l := range languages {
		if l.lines == nil {
			continue
		}
		for k, v := range l.lines {
			if !en[k] {
				t.Errorf("%s: line no longer said by services: %q", code, k)
			}
			if verbs(k) != verbs(v) {
				t.Errorf("%s: %q has verbs %q, the English %q", code, v, verbs(v), verbs(k))
			}
		}
		var missing []string
		for k := range en {
			if _, ok := l.lines[k]; !ok {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if out := os.Getenv("BEE_LANG_TODO"); out != "" {
			var b strings.Builder
			for _, k := range missing {
				fmt.Fprintf(&b, "%s = \"\"\n", strconv.Quote(k))
			}
			os.WriteFile(out, []byte(b.String()), 0o644)
		}
		if len(missing) > 0 {
			t.Errorf("%s: %d of %d lines not translated (BEE_LANG_TODO=file go test -run TestLanguageFiles lists them)", code, len(missing), len(en))
		}
	}
}
