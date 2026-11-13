// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "testing"

func TestUsers(t *testing.T) {
	c := ParseCfg(`users.status=enabled
users.1.name=Ab3xY
users.1.password=$6$salt$abcDEF./123
users.1.status=enabled
users.2.name=nobody
users.2.password=$6$x$y
users.2.shell=/bin/false
users.2.status=enabled
users.3.name=bad:name
users.3.password=$6$x$y
users.3.status=enabled
users.4.name=evil
users.4.password=$6$x$y:0:0
users.4.status=enabled
`)
	u, notes := Users(c)
	if len(u) != 1 || u[0].Name != "Ab3xY" || u[0].Hash != "$6$salt$abcDEF./123" {
		t.Fatalf("users %+v", u)
	}
	if len(notes) != 3 {
		t.Fatalf("notes %v", notes)
	}
}
