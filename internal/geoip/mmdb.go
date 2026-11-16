// Package geoip finds the country and network (ASN) of an IP address in
// MaxMind DB files: DB-IP Lite (downloaded monthly) or MaxMind GeoLite2.
package geoip

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"os"
)

// The MaxMind DB format: a binary search tree over the address bits, a
// 16-byte separator, a data section, and a metadata map at the end
// after a marker. https://maxmind.github.io/MaxMind-DB/

var metaMarker = []byte("\xab\xcd\xefMaxMind.com")

// DB is one MaxMind DB file in memory.
type DB struct {
	buf        []byte
	nodeCount  uint
	recordSize uint // bits per record: 24, 28 or 32
	ipVersion  uint
	dataStart  uint // offset of the data section
	v4Start    uint // the node IPv4 lookups start at (after 96 zero bits)
	// Type is the database type from the metadata ("DBIP-Country-Lite",
	// "GeoLite2-ASN"...).
	Type string
	// Built is when the file was made (seconds since 1970).
	Built uint64
}

// Open reads a MaxMind DB file.
func Open(path string) (*DB, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(buf)
}

// Parse reads a MaxMind DB from buf.
func Parse(buf []byte) (*DB, error) {
	i := bytes.LastIndex(buf, metaMarker)
	if i < 0 {
		return nil, errors.New("not a MaxMind DB file")
	}
	d := &decoder{buf: buf[i+len(metaMarker):]}
	v, _, err := d.decode(0, 0)
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	meta, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("metadata is not a map")
	}
	db := &DB{buf: buf[:i]}
	db.nodeCount = uint(num(meta["node_count"]))
	db.recordSize = uint(num(meta["record_size"]))
	db.ipVersion = uint(num(meta["ip_version"]))
	db.Type, _ = meta["database_type"].(string)
	db.Built = num(meta["build_epoch"])
	if db.recordSize != 24 && db.recordSize != 28 && db.recordSize != 32 {
		return nil, fmt.Errorf("record size %d", db.recordSize)
	}
	if db.ipVersion != 4 && db.ipVersion != 6 {
		return nil, fmt.Errorf("ip version %d", db.ipVersion)
	}
	treeSize := db.nodeCount * db.recordSize / 4
	if treeSize+16 > uint(len(db.buf)) || db.nodeCount == 0 {
		return nil, errors.New("search tree is larger than the file")
	}
	db.dataStart = treeSize + 16
	if db.ipVersion == 6 {
		node := uint(0)
		for i := 0; i < 96 && node < db.nodeCount; i++ {
			node = db.record(node, 0)
		}
		db.v4Start = node
	}
	return db, nil
}

func num(v any) uint64 {
	switch n := v.(type) {
	case uint64:
		return n
	case int64:
		return uint64(n)
	}
	return 0
}

// record reads the left (bit 0) or right (bit 1) record of node.
func (db *DB) record(node, bit uint) uint {
	b := db.buf[node*db.recordSize/4:]
	switch db.recordSize {
	case 24:
		b = b[bit*3:]
		return uint(b[0])<<16 | uint(b[1])<<8 | uint(b[2])
	case 28:
		if bit == 0 {
			return uint(b[3]&0xf0)<<20 | uint(b[0])<<16 | uint(b[1])<<8 | uint(b[2])
		}
		return uint(b[3]&0x0f)<<24 | uint(b[4])<<16 | uint(b[5])<<8 | uint(b[6])
	}
	return uint(binary.BigEndian.Uint32(b[bit*4:]))
}

// Lookup finds the record for ip: a map like {"country": {"iso_code":
// "NL"}}, or nil when the database has nothing for it.
func (db *DB) Lookup(ip netip.Addr) (map[string]any, error) {
	ip = ip.Unmap()
	var bits []byte
	node := uint(0)
	switch {
	case ip.Is4() && db.ipVersion == 6:
		a := ip.As4()
		bits, node = a[:], db.v4Start
	case ip.Is4():
		a := ip.As4()
		bits = a[:]
	case db.ipVersion == 4:
		return nil, nil // an IPv6 address in an IPv4-only database
	default:
		a := ip.As16()
		bits = a[:]
	}
	for i := 0; i < len(bits)*8 && node < db.nodeCount; i++ {
		node = db.record(node, uint(bits[i/8]>>(7-i%8)&1))
	}
	switch {
	case node == db.nodeCount:
		return nil, nil // no data for this address
	case node < db.nodeCount:
		return nil, errors.New("search tree ended inside the tree")
	}
	off := node - db.nodeCount - 16
	d := &decoder{buf: db.buf[db.dataStart:]}
	v, _, err := d.decode(off, 0)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	return m, nil
}

// decoder reads the data section's values.
type decoder struct{ buf []byte }

var errBad = errors.New("broken data section")

func (d *decoder) byteAt(off uint) (byte, error) {
	if off >= uint(len(d.buf)) {
		return 0, errBad
	}
	return d.buf[off], nil
}

func (d *decoder) bytes(off, n uint) ([]byte, error) {
	if off+n > uint(len(d.buf)) || off+n < off {
		return nil, errBad
	}
	return d.buf[off : off+n], nil
}

// decode reads the value at off; it returns the value and the offset
// after it. depth guards against loops in a broken file.
func (d *decoder) decode(off uint, depth int) (any, uint, error) {
	if depth > 32 {
		return nil, 0, errBad
	}
	ctrl, err := d.byteAt(off)
	if err != nil {
		return nil, 0, err
	}
	off++
	typ := uint(ctrl >> 5)
	if typ == 0 { // extended type
		b, err := d.byteAt(off)
		if err != nil {
			return nil, 0, err
		}
		typ, off = 7+uint(b), off+1
	}
	if typ == 1 { // pointer
		ss := uint(ctrl>>3) & 3
		vvv := uint(ctrl & 7)
		n := ss + 1
		b, err := d.bytes(off, n)
		if err != nil {
			return nil, 0, err
		}
		var p uint
		switch ss {
		case 0:
			p = vvv<<8 | uint(b[0])
		case 1:
			p = (vvv<<16 | uint(b[0])<<8 | uint(b[1])) + 2048
		case 2:
			p = (vvv<<24 | uint(b[0])<<16 | uint(b[1])<<8 | uint(b[2])) + 526336
		default:
			p = uint(binary.BigEndian.Uint32(b))
		}
		v, _, err := d.decode(p, depth+1)
		return v, off + n, err
	}
	size := uint(ctrl & 0x1f)
	if size >= 29 {
		n := size - 28
		b, err := d.bytes(off, n)
		if err != nil {
			return nil, 0, err
		}
		off += n
		switch n {
		case 1:
			size = 29 + uint(b[0])
		case 2:
			size = 285 + uint(b[0])<<8 | uint(b[1])
		default:
			size = 65821 + (uint(b[0])<<16 | uint(b[1])<<8 | uint(b[2]))
		}
	}
	switch typ {
	case 2: // UTF-8 string
		b, err := d.bytes(off, size)
		return string(b), off + size, err
	case 3: // double
		b, err := d.bytes(off, 8)
		if err != nil {
			return nil, 0, err
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b)), off + 8, nil
	case 4: // bytes
		b, err := d.bytes(off, size)
		return b, off + size, err
	case 5, 6, 9, 10, 8: // uint16, uint32, uint64, uint128, int32
		b, err := d.bytes(off, size)
		if err != nil || size > 16 {
			return nil, 0, errBad
		}
		var n uint64
		for _, c := range b {
			n = n<<8 | uint64(c)
		}
		if typ == 8 {
			return int64(int32(n)), off + size, nil
		}
		return n, off + size, nil
	case 7: // map
		m := make(map[string]any, min(size, 64))
		for i := uint(0); i < size; i++ {
			k, next, err := d.decode(off, depth+1)
			if err != nil {
				return nil, 0, err
			}
			key, ok := k.(string)
			if !ok {
				return nil, 0, errBad
			}
			v, after, err := d.decode(next, depth+1)
			if err != nil {
				return nil, 0, err
			}
			m[key], off = v, after
		}
		return m, off, nil
	case 11: // array
		a := make([]any, 0, min(size, 1024))
		for i := uint(0); i < size; i++ {
			v, after, err := d.decode(off, depth+1)
			if err != nil {
				return nil, 0, err
			}
			a, off = append(a, v), after
		}
		return a, off, nil
	case 14: // boolean
		return size != 0, off, nil
	case 15: // float
		b, err := d.bytes(off, 4)
		if err != nil {
			return nil, 0, err
		}
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), off + 4, nil
	}
	return nil, 0, errBad
}
