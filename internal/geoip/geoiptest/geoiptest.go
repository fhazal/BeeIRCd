// Package geoiptest writes small MaxMind DB files for tests.
package geoiptest

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"time"
)

// A tiny MaxMind DB writer, for tests.

// Enc writes one data-section value.
func Enc(v any) []byte {
	head := func(typ, size int) []byte {
		var b []byte
		ctrl := byte(0)
		if typ <= 7 {
			ctrl = byte(typ << 5)
		}
		switch {
		case size < 29:
			ctrl |= byte(size)
			b = append(b, ctrl)
		default:
			ctrl |= 29
			b = append(b, ctrl)
		}
		if typ > 7 {
			b = append(b, byte(typ-7))
		}
		if size >= 29 {
			b = append(b, byte(size-29))
		}
		return b
	}
	switch x := v.(type) {
	case string:
		return append(head(2, len(x)), x...)
	case uint32:
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], x)
		return append(head(6, 4), n[:]...)
	case uint64:
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], x)
		return append(head(9, 8), n[:]...)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := head(7, len(x))
		for _, k := range keys {
			out = append(out, Enc(k)...)
			out = append(out, Enc(x[k])...)
		}
		return out
	}
	panic("enc: unknown type")
}

// Net is one network and its record.
type Net struct {
	P    string
	Data map[string]any
}

// Build writes a MaxMind DB holding nets.
func Build(recordSize int, version int, nets []Net) []byte {
	const empty = -1
	type node [2]int
	nodes := []node{{empty, empty}}
	var data []byte
	refs := map[int]int{} // -(i+2) -> data offset
	for i, n := range nets {
		p := netip.MustParsePrefix(n.P)
		var bits []byte
		plen := p.Bits()
		if p.Addr().Is4() {
			a := p.Addr().As4()
			bits = a[:]
			if version == 6 {
				bits = append(make([]byte, 12), bits...)
				plen += 96
			}
		} else {
			a := p.Addr().As16()
			bits = a[:]
		}
		refs[i] = len(data)
		data = append(data, Enc(n.Data)...)
		cur := 0
		for b := 0; b < plen; b++ {
			bit := int(bits[b/8] >> (7 - b%8) & 1)
			if b == plen-1 {
				nodes[cur][bit] = -(i + 2)
				break
			}
			if nodes[cur][bit] == empty {
				nodes = append(nodes, node{empty, empty})
				nodes[cur][bit] = len(nodes) - 1
			}
			cur = nodes[cur][bit]
		}
	}
	count := len(nodes)
	val := func(r int) uint32 {
		switch {
		case r == empty:
			return uint32(count)
		case r >= 0:
			return uint32(r)
		}
		return uint32(count + 16 + refs[-r-2])
	}
	var tree []byte
	for _, n := range nodes {
		l, r := val(n[0]), val(n[1])
		switch recordSize {
		case 24:
			tree = append(tree, byte(l>>16), byte(l>>8), byte(l), byte(r>>16), byte(r>>8), byte(r))
		case 28:
			tree = append(tree, byte(l>>16), byte(l>>8), byte(l), byte(l>>24<<4|r>>24&0x0f), byte(r>>16), byte(r>>8), byte(r))
		case 32:
			tree = binary.BigEndian.AppendUint32(tree, l)
			tree = binary.BigEndian.AppendUint32(tree, r)
		}
	}
	out := append(tree, make([]byte, 16)...)
	out = append(out, data...)
	out = append(out, "\xab\xcd\xefMaxMind.com"...)
	return append(out, Enc(map[string]any{
		"node_count": uint32(count), "record_size": uint32(recordSize), "ip_version": uint32(version),
		"database_type": "Test-Country", "build_epoch": uint64(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Unix()),
	})...)
}

// Country is a country record.
func Country(cc, name string) map[string]any {
	return map[string]any{"country": map[string]any{"iso_code": cc, "names": map[string]any{"en": name}}}
}
