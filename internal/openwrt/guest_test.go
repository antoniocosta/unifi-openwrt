// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"strings"
	"testing"

	"unifi-openwrt/internal/translate"
)

func TestGuestRuleset(t *testing.T) {
	g := translate.Guest{Restricted: []string{"192.168.0.0/16", "10.0.0.0/8"}}
	rs, err := guestRuleset([]string{"phy0-ap2", "phy1-ap2"}, g, []string{"192.168.1.53", "192.168.1.1"}, "08:55:66:77:00:01")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"phy0-ap2", "phy1-ap2"`, "192.168.0.0/16, 10.0.0.0/8", "192.168.1.53, 192.168.1.1", "ip daddr @restricted drop", "ether daddr set 08:55:66:77:00:01"} {
		if !strings.Contains(rs, want) {
			t.Errorf("missing %q in\n%s", want, rs)
		}
	}
	if _, err := guestRuleset([]string{"x; flush ruleset"}, g, nil, ""); err == nil {
		t.Fatal("bad ifname accepted")
	}
	if _, err := guestRuleset([]string{"a"}, translate.Guest{Restricted: []string{"1.2.3.0/24 }"}}, nil, ""); err == nil {
		t.Fatal("bad prefix accepted")
	}
}

func TestGuestPortalRuleset(t *testing.T) {
	g := translate.Guest{Restricted: []string{"192.168.0.0/16"}, Portal: true, PortalURL: "http://192.168.1.1:8880/guest/s/default/", PortalHost: "192.168.1.1"}
	rs, err := guestRuleset([]string{"phy0-ap2"}, g, []string{"192.168.1.53"}, "08:55:66:77:00:01")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ip daddr 192.168.1.1 tcp dport { 8880, 8843, 8881, 8882 } accept", "ether saddr @authorized accept\n\t\tdrop", "ether saddr != @authorized ip daddr != 192.168.1.1 tcp dport 80 meta mark set 0x10000000"} {
		if !strings.Contains(rs, want) {
			t.Errorf("missing %q in\n%s", want, rs)
		}
	}
	if strings.Contains(rs, "%!") {
		t.Errorf("format error:\n%s", rs)
	}
}

// GUEST_NFT_OUT=file go test -run TestGuestDump: writes the portal ruleset
// for `nft -c -f` on a real AP.
func TestGuestDump(t *testing.T) {
	out := os.Getenv("GUEST_NFT_OUT")
	if out == "" {
		t.Skip("GUEST_NFT_OUT not set")
	}
	g := translate.Guest{Restricted: []string{"192.168.0.0/16", "10.0.0.0/8"}, Portal: true, PortalURL: "http://192.168.1.1:8880/guest/s/default/", PortalHost: "192.168.1.1"}
	rs, _ := guestRuleset([]string{"nosuch-ap9"}, g, []string{"192.168.1.53"}, "08:55:66:77:00:01")
	os.WriteFile(out, []byte(strings.Replace(rs, "unifi_guest", "unifi_guest_check", -1)), 0o644)
}
