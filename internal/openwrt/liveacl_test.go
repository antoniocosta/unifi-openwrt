// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"reflect"
	"strings"
	"testing"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

func TestLiveACL(t *testing.T) {
	acls := []translate.ACL{
		{Section: "s1", Policy: "deny", MACs: []string{"02:00:00:00:00:01"}},
		{Section: "s2"},
		{Section: "s3", Policy: "allow", MACs: []string{"02:00:00:00:00:03"}},
	}
	set := func(sec, opt, v, old string) translate.Op {
		return translate.Op{Key: "wireless." + sec + "." + opt, Value: v, Old: old}
	}
	del := func(sec, opt, old string) translate.Op {
		return translate.Op{Key: "wireless." + sec + "." + opt, Del: true, Old: old}
	}
	list := func(sec string, macs ...string) translate.Op {
		return translate.Op{Key: "wireless." + sec + ".maclist", List: macs}
	}
	// s3 wants no filter: for the switch from allow to none
	aclsNone := []translate.ACL{{Section: "s3"}}
	for name, c := range map[string]struct {
		ops  []translate.Op
		want []string // the sections returned; nil = not live
		acls []translate.ACL
	}{
		"lock added (none to deny)":   {[]translate.Op{set("s1", "macfilter", "deny", ""), list("s1", "02:00:00:00:00:01")}, []string{"s1"}, nil},
		"lock removed (deny to none)": {[]translate.Op{del("s2", "macfilter", "deny"), del("s2", "maclist", "02:00:00:00:00:01")}, []string{"s2"}, nil},
		"deny list edited":            {[]translate.Op{list("s1", "02:00:00:00:00:01")}, []string{"s1"}, nil},
		"allow list edited":           {[]translate.Op{list("s3", "02:00:00:00:00:03")}, []string{"s3"}, nil},
		"two sections, in acls order": {[]translate.Op{list("s3", "02:00:00:00:00:03"), list("s1", "02:00:00:00:00:01")}, []string{"s1", "s3"}, nil},
		"deny to allow":               {[]translate.Op{set("s3", "macfilter", "allow", "deny"), list("s3", "02:00:00:00:00:03")}, nil, nil},
		"none to allow":               {[]translate.Op{set("s3", "macfilter", "allow", ""), list("s3", "02:00:00:00:00:03")}, nil, nil},
		"allow to deny":               {[]translate.Op{set("s1", "macfilter", "deny", "allow")}, nil, nil},
		"plus another option":         {[]translate.Op{list("s1", "02:00:00:00:00:01"), set("s1", "disabled", "1", "0")}, nil, nil},
		"plus transmit power":         {[]translate.Op{list("s1", "02:00:00:00:00:01"), set("radio0", "txpower", "17", "18")}, nil, nil},
		"unknown section":             {[]translate.Op{list("s9", "02:00:00:00:00:01")}, nil, nil},
		"section created":             {[]translate.Op{{Key: "wireless.s1", Value: "wifi-iface"}}, nil, nil},
		"other package":               {[]translate.Op{{Key: "network.lan.proto", Value: "dhcp"}}, nil, nil},
		"empty plan":                  {nil, nil, nil},
		"allow to none":               {[]translate.Op{del("s3", "macfilter", "allow"), del("s3", "maclist", "02:00:00:00:00:03")}, nil, aclsNone},
	} {
		use := c.acls
		if use == nil {
			use = acls
		}
		got, ok := LiveACL(c.ops, use)
		if c.want == nil {
			if ok {
				t.Errorf("%s: live, want not live", name)
			}
			continue
		}
		var secs []string
		for _, a := range got {
			secs = append(secs, a.Section)
		}
		if !ok || !reflect.DeepEqual(secs, c.want) {
			t.Errorf("%s: %v %v, want %v", name, secs, ok, c.want)
		}
	}
}

// Anything that could reach the shell unchecked is refused.
func TestLiveBlockRefuses(t *testing.T) {
	good := translate.ACL{Section: "s1", Policy: "deny", MACs: []string{"02:00:00:00:00:01"}}
	for name, c := range map[string]struct {
		acl  translate.ACL
		keep []string
	}{
		"bad MAC":        {translate.ACL{Section: "s1", Policy: "deny", MACs: []string{"02:00:00:00:00:01;reboot"}}, nil},
		"bad keep MAC":   {good, []string{"$(reboot)"}},
		"bad section":    {translate.ACL{Section: "s1;reboot", Policy: "deny"}, nil},
		"unknown policy": {translate.ACL{Section: "s1", Policy: "maybe"}, nil},
	} {
		if _, err := liveBlock([]translate.ACL{c.acl}, c.keep, "/var/run", "wifi reload"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, run := range []string{"/var/run'x", "/var/run\nx"} {
		if _, err := liveBlock([]translate.ACL{good}, nil, run, "wifi reload"); err == nil {
			t.Errorf("run %q accepted", run)
		}
	}
	// upper case is fine: hostapd and the controller agree on lower case
	b, err := liveBlock([]translate.ACL{{Section: "s1", Policy: "deny", MACs: []string{"02:00:00:00:00:0A"}}}, []string{"02:00:00:00:00:0B"}, "/var/run", "wifi reload")
	if err != nil || !strings.Contains(b, "02:00:00:00:00:0a") || !strings.Contains(b, "02:00:00:00:00:0b") {
		t.Errorf("upper case: %v\n%s", err, b)
	}
}

func TestLiveOutcome(t *testing.T) {
	for out, want := range map[string]string{
		"UOW-LIVE ok\napplied\n": "live",
		"UOW-LIVE reload no hostapd control socket for phy0-ap0\napplied\n": "reload: no hostapd control socket for phy0-ap0",
		"applied\n": "reload: no report from the live update",
	} {
		if got := liveOutcome(out); got != want {
			t.Errorf("%q: %q, want %q", out, got, want)
		}
	}
}

// The translator's ops and ACLs must agree with LiveACL about what is live.
func TestLiveACLFromPlan(t *testing.T) {
	base := `wireless.1.devname=wifi0ap0
wireless.1.id=x
wireless.1.status=enabled
wireless.1.ssid=t
wireless.1.parent=wifi0
wireless.1.usage=user
wireless.1.security=none
radio.1.phyname=wifi0
radio.1.mode=ng
macacl.status=enabled
macacl.1.status=enabled
macacl.1.devname=wifi0ap0
macacl.1.acl.status=enabled
`
	cur := "wireless.radio0=wifi-device\nwireless.radio0.band='2g'\nwireless.ap0=wifi-iface\nwireless.ap0.device='radio0'\nwireless.ap0.mode='ap'\nwireless.ap0.ssid='t'\n" +
		// the rest of the WLAN config, so that only MAC filter ops remain
		"wireless.ap0.encryption='none'\nwireless.ap0.network='lan'\nwireless.ap0.uapsd='0'\n"
	f := translate.Features{Phys: map[string]string{"wifi0": "2g"}}
	oneMAC := "macacl.1.acl.1.status=enabled\nmacacl.1.acl.1.mac=02:00:00:00:00:09\n"
	for name, c := range map[string]struct {
		cfg, cur string
		live     bool
	}{
		"lock added":    {base + "macacl.1.acl.policy=deny\n" + oneMAC, cur, true},
		"lock removed":  {base + "macacl.1.acl.policy=deny\n", cur + "wireless.ap0.macfilter='deny'\nwireless.ap0.maclist='02:00:00:00:00:09'\n", true},
		"none to allow": {base + "macacl.1.acl.policy=allow\n" + oneMAC, cur, false},
		"allow to none": {base + "macacl.1.acl.policy=deny\n", cur + "wireless.ap0.macfilter='allow'\nwireless.ap0.maclist='02:00:00:00:00:09'\n", false},
	} {
		r := translate.Plan(translate.ParseCfg(c.cfg), translate.ParseUCI(c.cur), f)
		if _, ok := LiveACL(r.Ops, r.ACLs); ok != c.live {
			t.Errorf("%s: live %v, want %v (ops %+v)", name, ok, c.live, r.Ops)
		}
	}
}

func TestSyncOutcome(t *testing.T) {
	if err := syncOutcome("UOW-LIVE ok\n"); err != nil {
		t.Errorf("ok: %v", err)
	}
	if err := syncOutcome("UOW-LIVE reload x y\n"); err == nil || err.Error() != "x y" {
		t.Errorf("reload: %v", err)
	}
	if err := syncOutcome(""); err == nil {
		t.Error("empty output: no error")
	}
}
