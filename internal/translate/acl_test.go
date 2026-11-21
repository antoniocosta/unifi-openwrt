// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
	"testing"
)

func TestACLsFromUCI(t *testing.T) {
	cur := ParseUCI(`wireless.a.macfilter='deny'
wireless.a.maclist='02:00:00:00:00:01' '02:00:00:00:00:02'
wireless.b.macfilter='allow'
wireless.b.maclist='02:00:00:00:00:03'
wireless.c.maclist='02:00:00:00:00:04'
wireless.d.macfilter='deny'
wireless.e.macfilter='deny'
wireless.e.macfile='/etc/macs'
wireless.f.macfilter='deny'
wireless.f.maclist='02:00:00:00:00:AA' '02:00:00:00:00:BB'
`)
	for name, c := range map[string]struct {
		secs []string
		want []ACL
	}{
		"deny two":      {[]string{"a"}, []ACL{{"a", "deny", []string{"02:00:00:00:00:01", "02:00:00:00:00:02"}}}},
		"allow one":     {[]string{"b"}, []ACL{{"b", "allow", []string{"02:00:00:00:00:03"}}}},
		"stale maclist": {[]string{"c"}, []ACL{{Section: "c"}}},
		"deny no list":  {[]string{"d"}, []ACL{{"d", "deny", nil}}},
		"macfile":       {[]string{"e"}, nil},
		"upper case":    {[]string{"f"}, []ACL{{"f", "deny", []string{"02:00:00:00:00:aa", "02:00:00:00:00:bb"}}}},
		"order kept":    {[]string{"b", "a"}, []ACL{{"b", "allow", []string{"02:00:00:00:00:03"}}, {"a", "deny", []string{"02:00:00:00:00:01", "02:00:00:00:00:02"}}}},
	} {
		if got := ACLsFromUCI(cur, c.secs); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, want %#v", name, got, c.want)
		}
	}
}
