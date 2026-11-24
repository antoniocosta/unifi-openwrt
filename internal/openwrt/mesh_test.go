// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os/exec"
	"strings"
	"testing"
)

func TestMeshScript(t *testing.T) {
	c := exec.Command("sh", "-n")
	c.Stdin = strings.NewReader(meshScript)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}
	// the order that prevents a loop: on cable, the link goes down first
	d := meshScript[strings.Index(meshScript, "drop() {"):]
	if strings.Index(d, `ip link set "$i" down`) > strings.Index(d, "uci -q delete") {
		t.Fatal("drop must take the link down before cleaning up")
	}
	// a cable that is the uplink must reset the wait for an uplink that does
	// not associate
	if !strings.Contains(meshScript, "if [ $ok = 1 ]; then\n\t\t\t\tlost=0 down=0 wait=60") ||
		!strings.Contains(meshScript, "if reach; then\n\t\t\t\tdead=0 wait=60") {
		t.Fatal("a cable that reaches the gateway must reset down and wait")
	}
	// link alone never removes the uplink: every drop for a cable follows a test
	for _, l := range strings.Split(meshScript, "\n") {
		if strings.Contains(l, `drop "wired uplink back"`) && !strings.Contains(l, "wire 3 || probe 2") && strings.TrimSpace(l) != `drop "wired uplink back"` {
			t.Fatalf("drop without a gateway test: %q", l)
		}
	}
	if strings.Contains(meshScript, "`") {
		t.Fatal("a backquote would end the Go string")
	}
	if meshSafe.MatchString("a'b") || meshSafe.MatchString("a\nb") {
		t.Fatal("quote/newline accepted")
	}
}
