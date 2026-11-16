package luamod

import (
	"strings"
	"testing"
	"time"
)

func TestSandbox(t *testing.T) {
	L := NewState()
	defer L.Close()
	for _, name := range []string{"io", "os", "package", "debug", "require", "dofile", "loadfile", "load", "loadstring", "collectgarbage", "print", "setfenv", "getfenv", "coroutine"} {
		if v := L.GetGlobal(name); v.String() != "nil" {
			t.Errorf("%s is reachable in a module: %v", name, v)
		}
	}
	if err := Run(L, "t.lua", `assert(#string.rep("ab", 10) == 20); assert(math.floor(2.5) == 2); assert(table.concat({"a","b"}, ",") == "a,b"); assert(string.dump == nil)`, time.Second); err != nil {
		t.Fatalf("the safe libraries: %v", err)
	}
	if err := Run(L, "t.lua", `return string.rep("x", 2000000)`, time.Second); err == nil {
		t.Error("string.rep made a 2 MB string")
	}
	start := time.Now()
	err := Run(L, "t.lua", `while true do end`, 30*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "ran longer than 30ms") {
		t.Errorf("endless loop: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("the endless loop ran for %s", d)
	}
}

func TestCheckSource(t *testing.T) {
	if bad := CheckSource("greet", Starter("greet")); len(bad) != 0 {
		t.Fatalf("the starter module has mistakes: %v", bad)
	}
	for name, c := range map[string]struct{ file, src, want string }{
		"syntax":    {"m", "module { name = \"m\", version = \"1\" }\non(\"join\", function(e)\n", "m.lua"},
		"no header": {"m", "on(\"join\", function(e) end)\n", "no module {"},
		"name":      {"m", "module { name = \"other\", version = \"1\" }\non(\"join\", function(e) end)\n", "does not match the file name m.lua"},
		"version":   {"m", "module { name = \"m\" }\non(\"join\", function(e) end)\n", "has no version"},
		"hook":      {"m", "module { name = \"m\", version = \"1\" }\non(\"on_join\", function(e) end)\n", "did you mean join?"},
		"planned":   {"m", "module { name = \"m\", version = \"1\" }\non(\"check_join\", function(e) end)\n", "planned but not in this version"},
		"command":   {"m", "module { name = \"m\", version = \"1\" }\ncommand(\"MY CMD\", {}, function() end)\n", "letters and digits only"},
		"nothing":   {"m", "module { name = \"m\", version = \"1\" }\n", "would do nothing"},
		"api":       {"m", "module { name = \"m\", version = \"1\", api = 9 }\non(\"join\", function(e) end)\n", "module interface 9"},
		"file name": {"My Module", "module { name = \"My Module\", version = \"1\" }\non(\"join\", function(e) end)\n", "can't be a module name"},
		"typo":      {"m", "module { name = \"m\", version = \"1\" }\nnotise(\"x\", \"y\")\n", "m.lua:2"},
	} {
		bad := strings.Join(CheckSource(c.file, c.src), " | ")
		if !strings.Contains(bad, c.want) {
			t.Errorf("%s: want %q in %q", name, c.want, bad)
		}
	}
}

func TestHookTable(t *testing.T) {
	for name, h := range Hooks {
		if h.About == "" || (h.Kind != Event && h.Kind != Check) {
			t.Errorf("hook %s is not described", name)
		}
		if (h.Kind == Check) != strings.HasPrefix(name, "check_") {
			t.Errorf("hook %s: check hooks, and only they, start with check_", name)
		}
	}
}
