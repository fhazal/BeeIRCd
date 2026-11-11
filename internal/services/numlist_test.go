package services

import (
	"reflect"
	"testing"
)

func TestNumList(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want []int
		ok   bool
	}{
		{"3", 5, []int{3}, true},
		{"2-4", 5, []int{2, 3, 4}, true},
		{"1,3,5-7", 6, []int{1, 3, 5, 6}, true},
		{"4,2,2", 5, []int{2, 4}, true},
		{"9", 5, []int{}, true},
		{"0-2", 5, []int{1, 2}, true},
		{"4-2", 5, nil, false},
		{"*!*@x", 5, nil, false},
		{"", 5, nil, false},
		{"1-", 5, nil, false},
	} {
		got, ok := numList(tc.in, tc.max)
		if ok != tc.ok || ok && !reflect.DeepEqual(got, tc.want) {
			t.Errorf("numList(%q, %d) = %v, %v; want %v, %v", tc.in, tc.max, got, ok, tc.want, tc.ok)
		}
	}
}
