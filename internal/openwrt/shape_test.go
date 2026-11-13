// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os/exec"
	"strings"
	"testing"
)

func TestShapeScript(t *testing.T) {
	sc, err := shapeScript(IfShape{Ifname: "phy1-ap2", DownKbps: 5000, UpKbps: 2000, Stations: []string{"02:00:00:00:00:a2"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{
		"tc class add dev phy1-ap2 parent 1: classid 1:10 htb rate 5000kbit ceil 5000kbit",
		"u32 match ether dst 02:00:00:00:00:a2 classid 1:10",
		"action mirred egress redirect dev ifb-phy1-ap2",
		"tc class add dev ifb-phy1-ap2 parent 1: classid 1:10 htb rate 2000kbit",
		"u32 match ether src 02:00:00:00:00:a2 classid 1:10",
	} {
		if !strings.Contains(sc, w) {
			t.Errorf("missing %q in\n%s", w, sc)
		}
	}
	c := exec.Command("sh", "-n")
	c.Stdin = strings.NewReader(sc)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}
	if _, err := shapeScript(IfShape{Ifname: "x", Stations: []string{"zz; reboot"}}); err == nil {
		t.Fatal("bad MAC accepted")
	}
}
