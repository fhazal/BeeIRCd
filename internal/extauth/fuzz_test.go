package extauth

import (
	"bufio"
	"bytes"
	"testing"
)

// A directory's answer never makes the reader panic or read without end.
func FuzzBER(f *testing.F) {
	f.Add(tlv(tagSequence, berInt(tagInt, 1), tlv(appBindResponse, berInt(tagEnum, 0), berString(tagOctets, ""), berString(tagOctets, ""))))
	f.Add(tlv(tagSequence, berInt(tagInt, 2), tlv(appSearchEntry, berString(tagOctets, "uid=ann"), tlv(tagSequence, tlv(tagSequence, berString(tagOctets, "mail"), tlv(tagSet, berString(tagOctets, "a@b.c")))))))
	f.Add([]byte{0x30, 0x83, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bufio.NewReader(bytes.NewReader(data))
		for i := 0; i < 8; i++ {
			e, err := readElement(r)
			if err != nil {
				return
			}
			walk(e, 0)
			resultOf(e)
		}
	})
}

func walk(e element, depth int) {
	e.int()
	if depth > 6 {
		return
	}
	kids, err := children(e.body)
	if err != nil {
		return
	}
	for _, k := range kids {
		walk(k, depth+1)
	}
}

// Any filter text is refused or encoded, never a panic.
func FuzzFilter(f *testing.F) {
	for _, s := range []string{"(uid=%s)", "(&(uid=a)(|(x=1)(!(y=*))))", `(a=\2a)`, "((", "(a=b", ")", "(a>=1)(b<=2)", "(cn=*a*b*)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		encodeFilter(s)
		escapeFilter(s)
		escapeDN(s)
	})
}
