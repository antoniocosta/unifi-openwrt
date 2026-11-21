// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
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
		// An 802.11ax radio (OpenWrt's default HE80) must not drop to HT80,
		// which wifi-scripts reject; HT has no 80 MHz.
		{"11naht80", "HE80"}: "HE80", {"11naht40", "HE80"}: "HE40", {"11naht80", "EHT160"}: "EHT80",
		{"11naht80", "HT40"}: "VHT80", {"11naht80", ""}: "VHT80",
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
	// without WNM a stale bss_transition (even '0') is deleted, not kept
	r := Plan(ParseCfg(c), ParseUCI(cur+"wireless.ap0.bss_transition='1'\n"), Features{})
	del := false
	for _, op := range r.Ops {
		if op.Key == "wireless.ap0.bss_transition" {
			if !op.Del {
				t.Errorf("unsupported bss_transition written: %+v", op)
			}
			del = op.Del
		}
	}
	if !del {
		t.Errorf("stale bss_transition not deleted: %v", r.Ops)
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

// Two 5 GHz radios: with Sections each reported radio sets its own
// wifi-device; without, both would land on the first 5 GHz one.
func TestRadioSections(t *testing.T) {
	c := cfg + "radio.3.phyname=wifi2\nradio.3.ieee_mode=11naht40\nradio.3.channel=149\n"
	f := Features{Phys: map[string]string{"wifi0": "2g", "wifi1": "5g", "wifi2": "5g"},
		Sections: map[string]string{"wifi0": "radio0", "wifi1": "radio1", "wifi2": "radio2"}}
	got := map[string]string{}
	for _, op := range Plan(ParseCfg(c), ParseUCI(cur), f).Ops {
		got[op.Key] = op.Value
	}
	if got["wireless.radio2.channel"] != "149" || got["wireless.radio1.channel"] == "149" {
		t.Errorf("wifi2's channel must go to radio2 only: %v", got)
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

func TestCountryCode(t *testing.T) {
	for _, c := range []struct{ per, global, cc string }{
		{"840", "", "US"}, {"", "528", "NL"}, {"724", "840", "ES"}, {"392", "", "JP"},
		{"056", "", "BE"}, {"36", "", "AU"}, {"9999", "", ""}, {"", "", ""}, {"0", "", ""},
	} {
		if cc, _ := countryCode(c.per, c.global); cc != c.cc {
			t.Errorf("countryCode(%q, %q) = %q, want %q", c.per, c.global, cc, c.cc)
		}
	}
	if len(countryAlpha2) < 240 {
		t.Errorf("country table has only %d entries", len(countryAlpha2))
	}
	// an unknown code leaves the country alone and says so
	r := Plan(ParseCfg(strings.ReplaceAll(cfg, "countrycode=840", "countrycode=9999")), ParseUCI(cur), Features{})
	for _, op := range r.Ops {
		if strings.HasSuffix(op.Key, ".country") {
			t.Errorf("unknown country code: unexpected op %+v", op)
		}
	}
	if !strings.Contains(strings.Join(r.Notes, "\n"), "unknown country code") {
		t.Errorf("unknown country code: no note in %q", r.Notes)
	}
}

// A security type that is not built (WPA-Enterprise, OWE) must never come up
// as WPA2-PSK: an existing interface is switched off, a new one is not made.
func TestUnsupportedSecurityStaysOff(t *testing.T) {
	for _, mgmt := range []string{"WPA-EAP", "OWE", "WPA-EAP-SUITE-B-192"} {
		c := strings.Replace(cfg, "aaa.1.wpa.key.1.mgmt=SAE", "aaa.1.wpa.key.1.mgmt="+mgmt, 1) +
			"aaa.4.ssid=corp\naaa.4.wpa=2\naaa.4.wpa.key.1.mgmt=" + mgmt + "\nwireless.4.ssid=corp\nwireless.4.parent=wifi1\nwireless.4.usage=user\n"
		r := Plan(ParseCfg(c), ParseUCI(cur), Features{})
		off, n := false, 0
		for _, op := range r.Ops {
			switch {
			case op.Key == "wireless.ap0.disabled" && op.Value == "1":
				off = true
				n++
			case strings.HasPrefix(op.Key, "wireless.ap0."):
				t.Errorf("%s: existing interface changed: %+v", mgmt, op)
			case op.Value == "wifi-iface" && !strings.Contains(op.Key, "radio0_2"):
				t.Errorf("%s: interface created: %+v", mgmt, op)
			}
		}
		if !off || n != 1 {
			t.Errorf("%s: existing interface not switched off exactly once (%d)", mgmt, n)
		}
		if got := strings.Count(strings.Join(r.Notes, "\n"), "] is not supported"); got != 2 {
			t.Errorf("%s: %d notes, want 2: %q", mgmt, got, r.Notes)
		}
	}
	// WEP and Enhanced Open arrive without wpa rows: never an open network
	for _, c := range []Cfg{{"aaa.1.security": "wep64"}, {"aaa.1.security": "aes128"}, {"aaa.1.wpa3.support": "enabled", "aaa.1.wpa3.transition": "enabled"}} {
		if enc, note := encryption(c, "aaa.1."); enc != "" || note == "" {
			t.Errorf("%v: enc %q note %q", c, enc, note)
		}
	}
	for _, c := range []Cfg{{}, {"aaa.1.security": "none"}, {"aaa.1.wpa3.support": "disabled"}} {
		if enc, _ := encryption(c, "aaa.1."); enc != "none" {
			t.Errorf("%v: enc %q, want none", c, enc)
		}
	}
	// no key mgmt listed but a passphrase: still WPA2-PSK, as before
	if enc, _ := encryption(Cfg{"aaa.1.wpa": "2", "aaa.1.wpa.psk": "x"}, "aaa.1."); enc != "psk2+ccmp" {
		t.Errorf("no mgmt: %q", enc)
	}
}

func TestTxPowerMode(t *testing.T) {
	// radio0 (wifi0) currently has txpower 23 in `cur`
	rng := map[string][2]int{"wifi0": {6, 20}}
	for _, c := range []struct {
		name, lines string
		tx          map[string][2]int
		want        string // "" = txpower deleted
		note        bool
	}{
		{"medium", "mode=medium\nradio.1.txpower=auto", rng, "14", false},
		{"low", "mode=low\nradio.1.txpower=auto", rng, "8", false},
		{"low clamps to min", "mode=low\nradio.1.txpower=auto", map[string][2]int{"wifi0": {10, 20}}, "10", false},
		{"clamps to 1", "mode=low\nradio.1.txpower=auto", map[string][2]int{"wifi0": {0, 8}}, "1", false},
		{"custom", "mode=custom\nradio.1.txpower=10", rng, "10", false},
		{"high", "mode=high\nradio.1.txpower=auto", rng, "", false},
		{"auto", "mode=auto\nradio.1.txpower=auto", rng, "", false},
		{"no mode, auto", "radio.1.txpower=auto", rng, "", false},
		{"legacy numeric", "radio.1.txpower=12", rng, "12", false},
		{"stale numeric ignored in medium", "mode=medium\nradio.1.txpower=23", rng, "14", false},
		{"medium, unknown max: maximum + note", "mode=medium\nradio.1.txpower=auto", nil, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := strings.ReplaceAll(c.lines, "mode=", "radio.1.txpower_mode=")
			// drop the base cfg's own radio.1.txpower line
			base := strings.Replace(cfg, "radio.1.txpower=23\n", "", 1)
			r := Plan(ParseCfg(base+l+"\n"), ParseUCI(cur), Features{TxPower: c.tx})
			got, deleted := "", false
			for _, op := range r.Ops {
				if op.Key == "wireless.radio0.txpower" {
					got, deleted = op.Value, op.Del
				}
			}
			if c.want == "" && !deleted || c.want != "" && got != c.want {
				t.Errorf("txpower = %q (deleted %v), want %q; ops %v", got, deleted, c.want, r.Ops)
			}
			if c.note != strings.Contains(strings.Join(r.Notes, "\n"), "txpower") {
				t.Errorf("note mismatch: %v", r.Notes)
			}
		})
	}
}

func TestPlanFT(t *testing.T) {
	const key = "00112233445566778899AABBCCDDEEFF"
	ft := cfg + "aaa.1.ft.status=enabled\naaa.1.iapp_key=" + key + "\n"
	find := func(r Result, opt string) (Op, bool) {
		for _, op := range r.Ops {
			if op.Key == "wireless.ap0."+opt {
				return op, true
			}
		}
		return Op{}, false
	}
	// with the relay: the UniFi's mobility domain, pulled keys, the wildcard
	// key holders with the controller's key
	r := Plan(ParseCfg(ft), ParseUCI(cur), Features{R: true, FTRelay: true})
	for opt, want := range map[string]string{"ieee80211r": "1", "mobility_domain": MobilityDomain("home"), "pmk_r1_push": "1", "ft_psk_generate_local": "0"} {
		if op, ok := find(r, opt); !ok || op.Value != want {
			t.Errorf("%s = %q (found %v), want %q", opt, op.Value, ok, want)
		}
	}
	lower := strings.ToLower(key)
	for opt, want := range map[string]string{"r0kh": "ff:ff:ff:ff:ff:ff,*," + lower, "r1kh": "00:00:00:00:00:00,00:00:00:00:00:00," + lower} {
		op, ok := find(r, opt)
		if !ok || len(op.List) != 1 || op.List[0] != want || !op.Secret {
			t.Errorf("%s: %+v", opt, op)
		}
		if strings.Contains(op.String(), lower) {
			t.Errorf("%s: the key shows in the plan: %s", opt, op)
		}
	}
	// applied: nothing left to do
	after := cur + "wireless.ap0.ieee80211r='1'\nwireless.ap0.mobility_domain='" + MobilityDomain("home") + "'\nwireless.ap0.pmk_r1_push='1'\nwireless.ap0.ft_psk_generate_local='0'\n" +
		"wireless.ap0.r0kh='ff:ff:ff:ff:ff:ff,*," + lower + "'\nwireless.ap0.r1kh='00:00:00:00:00:00,00:00:00:00:00:00," + lower + "'\n"
	r = Plan(ParseCfg(ft), ParseUCI(after), Features{R: true, FTRelay: true})
	for _, opt := range []string{"mobility_domain", "pmk_r1_push", "ft_psk_generate_local", "r0kh", "r1kh"} {
		if op, ok := find(r, opt); ok {
			t.Errorf("already applied, but: %s", op)
		}
	}
	// the relay is gone (or fast roaming switched off): back to OpenWrt's
	// defaults, with a note in the first case
	for name, c := range map[string]struct {
		cfg  string
		f    Features
		note bool
	}{
		"no relay":         {ft, Features{R: true}, true},
		"fast roaming off": {cfg + "aaa.1.iapp_key=" + key + "\n", Features{R: true, FTRelay: true}, false},
		"no key sent":      {cfg + "aaa.1.ft.status=enabled\n", Features{R: true, FTRelay: true}, false},
	} {
		r = Plan(ParseCfg(c.cfg), ParseUCI(after), c.f)
		for _, opt := range []string{"mobility_domain", "pmk_r1_push", "ft_psk_generate_local", "r0kh", "r1kh"} {
			if op, ok := find(r, opt); !ok || !op.Del {
				t.Errorf("%s: %s not deleted (%+v)", name, opt, op)
			}
		}
		if got := strings.Contains(strings.Join(r.Notes, "\n"), "kmod-veth"); got != c.note {
			t.Errorf("%s: note %v, want %v: %v", name, got, c.note, r.Notes)
		}
	}
	// a fresh AP without the relay: nothing about FT keys is written
	r = Plan(ParseCfg(ft), ParseUCI(cur), Features{R: true})
	for _, opt := range []string{"mobility_domain", "pmk_r1_push", "ft_psk_generate_local", "r0kh", "r1kh"} {
		if op, ok := find(r, opt); ok {
			t.Errorf("no relay, fresh AP: %s", op)
		}
	}
	if MobilityDomain("example") != "56a5" { // SHA-1("salt" + "example" + 25 zero bytes) starts 56 a5
		t.Errorf("mobility domain of example: %s", MobilityDomain("example"))
	}
}

func TestPlanFTSiblings(t *testing.T) {
	ft := cfg + "aaa.1.ft.status=enabled\naaa.1.iapp_key=00112233445566778899aabbccddeeff\naaa.1.wpa3.support=enabled\naaa.1.wpa3.transition=enabled\n"
	ft = strings.Replace(ft, "aaa.1.wpa.key.1.mgmt=WPA-PSK", "aaa.1.wpa.key.1.mgmt=SAE", 1)
	opts := func(f Features) string {
		for _, op := range Plan(ParseCfg(ft), ParseUCI(cur), f).Ops {
			if op.Key == "wireless.ap0.hostapd_bss_options" {
				return strings.Join(op.List, "|")
			}
		}
		return ""
	}
	lack := map[string]bool{"home": true}
	for name, c := range map[string]struct {
		f    Features
		want string
	}{
		"nothing known":             {Features{R: true, FTRelay: true}, "SAE FT-SAE WPA-PSK FT-PSK"},
		"siblings without FT-PSK":   {Features{R: true, FTRelay: true, NoFTPSK: lack}, "SAE FT-SAE WPA-PSK"},
		"siblings without FT-SAE":   {Features{R: true, FTRelay: true, NoFTSAE: lack}, "SAE WPA-PSK FT-PSK"},
		"another SSID's siblings":   {Features{R: true, FTRelay: true, NoFTPSK: map[string]bool{"other": true}}, "SAE FT-SAE WPA-PSK FT-PSK"},
		"no relay: own domain, all": {Features{R: true, NoFTPSK: lack}, "SAE FT-SAE WPA-PSK FT-PSK"},
	} {
		if got := opts(c.f); got != "wpa_key_mgmt="+c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// Result.ACLs holds the wanted MAC filter of every planned WLAN section,
// changed or not (openwrt.LiveACL decides from it).
func TestPlanACLs(t *testing.T) {
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
	for name, c := range map[string]struct {
		cfg, cur string
		want     []ACL
	}{
		"deny two": {
			base + "macacl.1.acl.policy=deny\nmacacl.1.acl.1.status=enabled\nmacacl.1.acl.1.mac=02:00:00:00:00:09\nmacacl.1.acl.2.status=enabled\nmacacl.1.acl.2.mac=02:00:00:00:00:0A\n",
			cur,
			[]ACL{{Section: "ap0", Policy: "deny", MACs: []string{"02:00:00:00:00:09", "02:00:00:00:00:0a"}}},
		},
		"empty deny is no filter": {
			base + "macacl.1.acl.policy=deny\n",
			cur + "wireless.ap0.macfilter='deny'\nwireless.ap0.maclist='02:00:00:00:00:09'\n",
			[]ACL{{Section: "ap0"}},
		},
		"allow, already applied": {
			base + "macacl.1.acl.policy=allow\nmacacl.1.acl.1.status=enabled\nmacacl.1.acl.1.mac=02:00:00:00:00:09\n",
			cur + "wireless.ap0.macfilter='allow'\nwireless.ap0.maclist='02:00:00:00:00:09'\n",
			[]ACL{{Section: "ap0", Policy: "allow", MACs: []string{"02:00:00:00:00:09"}}},
		},
		"filter off": {
			strings.Replace(base, "macacl.status=enabled", "macacl.status=disabled", 1),
			cur,
			[]ACL{{Section: "ap0"}},
		},
		"macfile section is left out": {
			base + "macacl.1.acl.policy=deny\nmacacl.1.acl.1.status=enabled\nmacacl.1.acl.1.mac=02:00:00:00:00:09\n",
			cur + "wireless.ap0.macfile='/etc/macs'\n",
			nil,
		},
	} {
		if got := Plan(ParseCfg(c.cfg), ParseUCI(c.cur), f).ACLs; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", name, got, c.want)
		}
	}
}
