// Package anopedb reads the database Anope 2.0 writes with db_flatfile
// (anope.db) and brings it into BeeIRCd's services database.
//
// The file is a list of objects:
//
//	OBJECT NickCore
//	ID 12                      (optional)
//	DATA display alice
//	DATA pass sha256:...:...
//	END
//
// A DATA line is "DATA <key> <value>"; the value is the rest of the line
// and may be empty or hold spaces.
package anopedb

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// Object is one record: its type (NickCore, ChannelInfo, ...) and fields.
type Object struct {
	Type string
	Line int // where OBJECT was, for messages
	Data map[string]string
}

// Get returns field k ("" when missing).
func (o Object) Get(k string) string { return o.Data[k] }

// Bool reads a flag field: Anope writes 1 for on.
func (o Object) Bool(k string) bool {
	v := strings.TrimSpace(o.Data[k])
	return v != "" && v != "0"
}

// Int reads a number field (0 when missing or not a number).
func (o Object) Int(k string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(o.Data[k]), 10, 64)
	return n
}

// Time reads a Unix time field; 0 or missing is the zero time.
func (o Object) Time(k string) time.Time {
	if n := o.Int(k); n > 0 {
		return time.Unix(n, 0)
	}
	return time.Time{}
}

// Words splits a space-separated list field.
func (o Object) Words(k string) []string { return strings.Fields(o.Data[k]) }

// Parse reads a whole anope.db.
func Parse(r io.Reader) ([]Object, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var (
		out []Object
		cur *Object
		n   int
	)
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "OBJECT "):
			if cur != nil {
				return nil, fmt.Errorf("line %d: OBJECT before the END of the one on line %d", n, cur.Line)
			}
			cur = &Object{Type: strings.TrimSpace(line[len("OBJECT "):]), Line: n, Data: map[string]string{}}
		case line == "END":
			if cur == nil {
				return nil, fmt.Errorf("line %d: END without OBJECT", n)
			}
			out = append(out, *cur)
			cur = nil
		case strings.HasPrefix(line, "ID "):
			if cur == nil {
				return nil, fmt.Errorf("line %d: ID outside an object", n)
			}
		case strings.HasPrefix(line, "DATA "):
			if cur == nil {
				return nil, fmt.Errorf("line %d: DATA outside an object", n)
			}
			kv := line[len("DATA "):]
			k, v, ok := strings.Cut(kv, " ")
			if !ok || k == "" {
				continue // Anope skips these too
			}
			cur.Data[k] = v
		default:
			return nil, fmt.Errorf("line %d: not an Anope 2.0 database line: %.40q", n, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if cur != nil {
		return nil, fmt.Errorf("the object on line %d has no END (file cut short?)", cur.Line)
	}
	return out, nil
}
