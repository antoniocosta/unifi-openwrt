// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"strings"
	"testing"
)

const cur = `wireless.radio0=wifi-device
wireless.radio0.band='2g'
wireless.radio0.channel='6'
wireless.radio0.htmode='HT20'
wireless.radio0.txpower='23'
wireless.radio0.country='US'
wireless.radio1=wifi-device
wireless.radio1.band='5g'
wireless.radio1.channel='36'
wireless.radio1.htmode='VHT80'
wireless.radio1.country='US'
wireless.radio2=wifi-device
wireless.radio2.band='5g'
wireless.radio2.disabled='1'
wireless.ap0=wifi-iface
wireless.ap0.device='radio0'
wireless.ap0.mode='ap'
wireless.ap0.network='lan'
wireless.ap0.ssid='home'
wireless.ap0.encryption='sae-mixed'
wireless.ap0.ieee80211w='1'
wireless.ap0.key='s3cret-pass'
`

const cfg = `radio.1.phyname=wifi0
radio.1.ieee_mode=11nght20
radio.1.channel=6
radio.1.txpower=23
radio.1.countrycode=840
radio.2.phyname=wifi1
radio.2.ieee_mode=11naht80
radio.2.channel=36
radio.2.txpower=23
radio.2.countrycode=840
aaa.1.ssid=home
aaa.1.wpa=2
aaa.1.wpa.key.1.mgmt=SAE
aaa.1.wpa3.support=enabled
aaa.1.wpa3.transition=enabled
aaa.1.pmf.status=enabled
aaa.1.pmf.mode=1
aaa.1.wpa.psk=s3cret-pass
wireless.1.ssid=home
wireless.1.parent=wifi0
wireless.1.usage=user
wireless.2.ssid=element-abc
wireless.2.parent=wifi0
wireless.2.hide_ssid=true
wireless.2.element_adopt=enabled
aaa.2.ssid=element-abc
aaa.2.wpa=2
aaa.2.wpa.key.1.mgmt=WPA-PSK
aaa.2.wpa.psk=elem-pass-1
wireless.3.ssid=vwire-x
wireless.3.parent=wifi1
wireless.3.vwire=enabled
mesh.status=disabled
`

func TestPlanMatchingConfigIsQuietExceptElement(t *testing.T) {
	r := Plan(ParseCfg(cfg), ParseUCI(cur), Features{})
	for _, op := range r.Ops {
		if strings.HasPrefix(op.Key, "wireless.ap0.") && (strings.Contains(op.Key, "encryption") || strings.Contains(op.Key, ".key")) || strings.Contains(op.Key, "channel") {
			t.Errorf("unexpected op: %s", op)
		}
		if strings.Contains(op.String(), "s3cret") {
			t.Errorf("secret leaked: %s", op)
		}
		if strings.Contains(op.Key, "vwire") || strings.Contains(op.Key, "radio2") {
			t.Errorf("should be skipped: %s", op)
		}
		if strings.Contains(op.String(), "elem-pass") {
			t.Errorf("secret leaked: %s", op)
		}
	}
	hidden := false
	for _, op := range r.Ops {
		if strings.HasSuffix(op.Key, ".hidden") && op.Value == "1" {
			hidden = true
		}
	}
	if !hidden {
		t.Errorf("element-adopt SSID not created hidden: %v", r.Ops)
	}
	if len(r.Skipped) == 0 || r.Skipped[0] != "mesh" {
		t.Errorf("skipped: %v", r.Skipped)
	}
}

func TestPlanChanges(t *testing.T) {
	c := strings.Replace(cfg, "radio.2.channel=36", "radio.2.channel=44", 1)
	c = strings.Replace(c, "aaa.1.wpa.psk=s3cret-pass", "aaa.1.wpa.psk=new-pass-123", 1)
	r := Plan(ParseCfg(c), ParseUCI(cur), Features{})
	var ch, key bool
	for _, op := range r.Ops {
		if op.Key == "wireless.radio1.channel" && op.Value == "44" {
			ch = true
		}
		if op.Key == "wireless.ap0.key" && op.Secret {
			key = true
			if strings.Contains(op.String(), "new-pass") {
				t.Errorf("secret leaked: %s", op)
			}
		}
	}
	if !ch || !key {
		t.Fatalf("ops: %v", r.Ops)
	}
}

func TestHTMode(t *testing.T) {
	for in, want := range map[[2]string]string{
		{"11nght20", "HT20"}: "HT20", {"11naht80", "VHT80"}: "VHT80", {"11naht40", "VHT80"}: "VHT40", {"11naht40", "HT40"}: "HT40",
	} {
		if got := htmode(in[0], in[1]); got != want {
			t.Errorf("htmode(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestClampUnsupported(t *testing.T) {
	c := cfg + "aaa.1.bss_transition=enabled\n"
	for _, f := range []Features{{}, {V: true}} {
		r := Plan(ParseCfg(c), ParseUCI(cur), f)
		set := false
		for _, op := range r.Ops {
			if op.Key == "wireless.ap0.bss_transition" && op.Value == "1" {
				set = true
			}
		}
		if set != f.V {
			t.Errorf("features %+v: bss_transition set=%v", f, set)
		}
	}
}

func TestPlanSystem(t *testing.T) {
	c := "system.timezone=EST5EDT,M3.2.0,M11.1.0\nntpclient.status=enabled\nntpclient.1.status=enabled\nntpclient.1.server=0.ubnt.pool.ntp.org\nntpclient.2.status=enabled\nntpclient.2.server=1.ubnt.pool.ntp.org\n"
	u := "system.@system[0]=system\nsystem.@system[0].timezone='GMT0'\nsystem.@system[0].zonename='UTC'\nsystem.ntp=timeserver\nsystem.ntp.server='0.openwrt.pool.ntp.org' '1.openwrt.pool.ntp.org'\n"
	r := Plan(ParseCfg(c), ParseUCI(u), Features{})
	var tz, zn, ntp bool
	for _, op := range r.Ops {
		switch {
		case op.Key == "system.@system[0].timezone" && op.Value == "EST5EDT,M3.2.0,M11.1.0":
			tz = true
		case op.Key == "system.@system[0].zonename" && op.Del:
			zn = true
		case op.Key == "system.ntp.server" && len(op.List) == 2:
			ntp = true
		}
	}
	if !tz || !zn || !ntp {
		t.Fatalf("ops: %v", r.Ops)
	}
	// already matching -> no ops
	u2 := "system.@system[0]=system\nsystem.@system[0].timezone='EST5EDT,M3.2.0,M11.1.0'\nsystem.ntp=timeserver\nsystem.ntp.server='0.ubnt.pool.ntp.org' '1.ubnt.pool.ntp.org'\n"
	if r := Plan(ParseCfg(c), ParseUCI(u2), Features{}); len(r.Ops) != 0 {
		t.Fatalf("expected no ops, got %v", r.Ops)
	}
}

func TestUnknownRadioIgnored(t *testing.T) {
	c := cfg + "radio.3.phyname=wifi2\nradio.3.ieee_mode=11naht40\nradio.3.channel=auto\nwireless.9.ssid=x\nwireless.9.parent=wifi2\n"
	r := Plan(ParseCfg(c), ParseUCI(cur), Features{Phys: map[string]string{"wifi0": "2g", "wifi1": "5g"}})
	for _, op := range r.Ops {
		if op.Key == "wireless.radio1.channel" || op.Key == "wireless.radio1.htmode" {
			t.Errorf("stale radio leaked into radio1: %s", op)
		}
	}
}

func TestMacACL(t *testing.T) {
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
	cur := "wireless.radio0=wifi-device\nwireless.radio0.band='2g'\nwireless.ap0=wifi-iface\nwireless.ap0.device='radio0'\nwireless.ap0.mode='ap'\nwireless.ap0.ssid='t'\n"
	f := Features{Phys: map[string]string{"wifi0": "2g"}}
	find := func(r Result, key string) *Op {
		for i := range r.Ops {
			if r.Ops[i].Key == key {
				return &r.Ops[i]
			}
		}
		return nil
	}
	// empty deny list = no filter
	r := Plan(ParseCfg(base+"macacl.1.acl.policy=deny\n"), ParseUCI(cur), f)
	if op := find(r, "wireless.ap0.macfilter"); op != nil {
		t.Fatalf("empty deny on an unfiltered iface: %+v", op)
	}
	r = Plan(ParseCfg(base+"macacl.1.acl.policy=deny\n"), ParseUCI(cur+"wireless.ap0.macfilter='deny'\nwireless.ap0.maclist='02:00:00:00:00:09'\n"), f)
	if op := find(r, "wireless.ap0.macfilter"); op == nil || !op.Del {
		t.Fatalf("macfilter not deleted (OpenWrt has no 'disable'): %+v", op)
	}
	if op := find(r, "wireless.ap0.maclist"); op == nil || !op.Del {
		t.Fatalf("maclist not deleted: %+v", op)
	}
	// deny two MACs
	c := base + "macacl.1.acl.policy=deny\nmacacl.1.acl.1.status=enabled\nmacacl.1.acl.1.mac=02:00:00:00:00:09\nmacacl.1.acl.2.status=enabled\nmacacl.1.acl.2.mac=02:00:00:00:00:0A\n"
	r = Plan(ParseCfg(c), ParseUCI(cur), f)
	if op := find(r, "wireless.ap0.macfilter"); op == nil || op.Value != "deny" {
		t.Fatalf("deny: %+v", op)
	}
	if op := find(r, "wireless.ap0.maclist"); op == nil || len(op.List) != 2 || op.List[1] != "02:00:00:00:00:0a" {
		t.Fatalf("maclist: %+v", op)
	}
	// already applied (one MAC, as uci show prints it): no ops
	c1 := base + "macacl.1.acl.policy=allow\nmacacl.1.acl.1.status=enabled\nmacacl.1.acl.1.mac=02:00:00:00:00:09\n"
	r = Plan(ParseCfg(c1), ParseUCI(cur+"wireless.ap0.macfilter='allow'\nwireless.ap0.maclist='02:00:00:00:00:09'\n"), f)
	if op := find(r, "wireless.ap0.maclist"); op != nil {
		t.Fatalf("unexpected %+v", op)
	}
}
