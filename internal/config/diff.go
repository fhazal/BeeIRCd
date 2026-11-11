package config

import (
	"fmt"
	"reflect"
	"strings"
)

// Change is one setting that differs between two configs.
type Change struct {
	Section string // TOML table, like "limits"; "" for top-level lists
	Key     string // TOML key, like "connect_rate"; a list's own name
	Old     string // "" for lists and secrets
	New     string
}

// String writes a change the way REHASH reports it: "connect_rate 5:60 →
// 10:60", "dnsbl lists", "cloak secret".
func (c Change) String() string {
	if c.Old == "" && c.New == "" {
		if c.Section == "" {
			return c.Key
		}
		return c.Section + " " + c.Key
	}
	return c.Key + " " + c.Old + " → " + c.New
}

// Diff lists what differs from a to b, section by section.
func Diff(a, b *Config) []Change {
	var out []Change
	av, bv := reflect.ValueOf(a).Elem(), reflect.ValueOf(b).Elem()
	t := av.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := tomlName(f)
		if name == "" || name == "-" {
			continue
		}
		x, y := av.Field(i), bv.Field(i)
		if f.Type.Kind() == reflect.Struct {
			out = append(out, diffStruct(name, x, y)...)
			continue
		}
		if !reflect.DeepEqual(x.Interface(), y.Interface()) {
			out = append(out, Change{Key: listName(name)})
		}
	}
	return out
}

func listName(name string) string {
	switch name {
	case "listen":
		return "ports"
	case "dnsbl":
		return "dnsbl lists"
	case "operclass":
		return "oper classes"
	}
	return name
}

func diffStruct(section string, x, y reflect.Value) []Change {
	var out []Change
	t := x.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := tomlName(f)
		if key == "" || key == "-" || !f.IsExported() {
			continue
		}
		a, b := x.Field(i), y.Field(i)
		if reflect.DeepEqual(a.Interface(), b.Interface()) {
			continue
		}
		ch := Change{Section: section, Key: key}
		secret := strings.Contains(key, "secret") || strings.Contains(key, "password")
		if as, ok := show(a); ok && !secret {
			bs, _ := show(b)
			ch.Old, ch.New = as, bs
		}
		out = append(out, ch)
	}
	return out
}

// show writes a plain value; lists, maps and structs give ok false.
func show(v reflect.Value) (string, bool) {
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "default", true
		}
		v = v.Elem()
	}
	if d, ok := v.Interface().(Duration); ok {
		return FormatDuration(d.Duration), true
	}
	switch v.Kind() {
	case reflect.String:
		if v.String() == "" {
			return `""`, true
		}
		return v.String(), true
	case reflect.Bool, reflect.Int, reflect.Int64, reflect.Uint, reflect.Float64:
		return fmt.Sprint(v.Interface()), true
	}
	return "", false
}

func tomlName(f reflect.StructField) string {
	tag := f.Tag.Get("toml")
	name, _, _ := strings.Cut(tag, ",")
	return name
}
