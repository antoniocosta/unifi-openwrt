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
	if meshSafe.MatchString("a'b") || meshSafe.MatchString("a\nb") {
		t.Fatal("quote/newline accepted")
	}
}
