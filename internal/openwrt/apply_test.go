// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

func TestUCIBatchQuoting(t *testing.T) {
	b, err := UCIBatch([]translate.Op{
		{Key: "wireless.ap0.dtim_period", Value: "1"},
		{Key: "wireless.ap0.key", Value: `it's "x"\y`, Secret: true},
		{Del: true, Key: "wireless.radio0.txpower"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "set wireless.ap0.dtim_period='1'\nset wireless.ap0.key=\"it's \\\"x\\\"\\\\y\"\ndelete wireless.radio0.txpower\n"
	if b != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b, want)
	}
	if _, err := UCIBatch([]translate.Op{{Key: "k", Value: "a\nb"}}); err == nil {
		t.Fatal("newline accepted")
	}
}

func TestWifiStatusOK(t *testing.T) {
	ok, _ := wifiStatusOK([]byte(`{"radio0":{"up":true,"disabled":false,"interfaces":[{}]},"radio2":{"up":false,"disabled":true,"interfaces":[]}}`))
	if !ok {
		t.Fatal("healthy status rejected")
	}
	ok, why := wifiStatusOK([]byte(`{"radio0":{"up":false,"disabled":false,"interfaces":[{}]}}`))
	if ok || !strings.Contains(why, "radio0") {
		t.Fatalf("down radio accepted: %s", why)
	}
	// a disabled device: radios without networks
	if ok, why := wifiStatusOK([]byte(`{"radio0":{"up":false,"disabled":false,"interfaces":[]}}`)); !ok {
		t.Fatalf("radio without networks rejected: %s", why)
	}
}

func TestUCIBatchList(t *testing.T) {
	b, err := UCIBatch([]translate.Op{{Key: "system.ntp.server", List: []string{"a.pool", "b.pool"}}})
	if err != nil {
		t.Fatal(err)
	}
	if b != "delete system.ntp.server\nadd_list system.ntp.server='a.pool'\nadd_list system.ntp.server='b.pool'\n" {
		t.Fatalf("%q", b)
	}
}

func TestBeepWAV(t *testing.T) {
	w := beepWAV()
	if string(w[:4]) != "RIFF" || string(w[8:16]) != "WAVEfmt " || string(w[36:40]) != "data" {
		t.Fatalf("bad header % x", w[:44])
	}
	if got := int(w[4]) | int(w[5])<<8 | int(w[6])<<16 | int(w[7])<<24; got != len(w)-8 {
		t.Fatalf("RIFF size %d, file %d", got, len(w))
	}
}

func TestApplyScriptRollback(t *testing.T) {
	_, sc, err := applyScript([]translate.Op{{Key: "wireless.radio0.txpower", Value: "20"}}, 300*time.Second, "ID1")
	if err != nil {
		t.Fatal(err)
	}
	rb := sc[strings.Index(sc, "<<'RB'"):strings.Index(sc, "\nRB\n")]
	ok := strings.Index(rb, "if [ -f /tmp/unifi-ok-ID1 ]; then")
	restore := strings.Index(rb, "cp -p /etc/config/wireless.pre-unifi-ID1 /etc/config/wireless")
	if ok < 0 || restore < ok || strings.Count(rb, "rm -f /etc/config/*.pre-unifi-ID1") != 2 {
		t.Fatalf("rollback script:\n%s", rb)
	}
	if strings.Contains(sc, "%!") {
		t.Fatalf("format error:\n%s", sc)
	}
	// the timer starts with or without setsid (BusyBox may lack the applet)
	if !strings.Contains(sc, "command -v setsid") || !strings.Contains(sc, `(trap "" HUP; exec sh /tmp/unifi-rollback-ID1.sh) </dev/null >/dev/null 2>&1 &`) {
		t.Fatalf("rollback timer has no fallback without setsid:\n%s", sc)
	}
}

// With no scan interface, iw fails (ENODEV, exit 237); under set -e that
// must not end the apply before the Wi-Fi reload.
func TestApplyScriptNoScanIface(t *testing.T) {
	_, sc, err := applyScript([]translate.Op{{Key: "wireless.radio0.channel", Value: "36"}}, time.Minute, "ID3")
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(sc, "\n") {
		if strings.Contains(l, "iw dev "+ScanIface+" del") && strings.Contains(l, "wifi reload") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no reload line in:\n%s", sc)
	}
	out, err := exec.Command("sh", "-c", "set -e; iw() { return 237; }; wifi() { echo reloaded; }; "+line).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "reloaded" {
		t.Errorf("%q: %v %s", line, err, out)
	}
}

// A wpad built without WNM still holds strings like "bss_transition_request";
// only a config key of its own means hostapd takes bss_transition.
func TestFeatureScript11v(t *testing.T) {
	for body, want := range map[string]bool{
		"#!/bin/sh\nexit 1\n# bss_transition_request\n": false,
		"#!/bin/sh\nexit 1\nbss_transition\n":           true,
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "hostapd"), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		c := exec.Command("sh", "-c", featureScript)
		c.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
		out, err := c.Output()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(out), "11v"); got != want {
			t.Errorf("%q: 11v = %v, want %v", body, got, want)
		}
	}
}

func TestApplyScriptSyntax(t *testing.T) {
	_, sc, err := applyScript([]translate.Op{{Key: "wireless.radio0.txpower", Value: "20"}, {Key: "network.lan.proto", Value: "dhcp"}, {Key: "system.@system[0].timezone", Value: "EST5EDT,M3.2.0,M11.1.0"}}, time.Minute, "ID2")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{sc, sc[strings.Index(sc, "<<'RB'\n")+7 : strings.Index(sc, "\nRB\n")]} {
		c := exec.Command("sh", "-n")
		c.Stdin = strings.NewReader(s)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("sh -n: %v %s\n%s", err, out, s)
		}
	}
}

// A live plan gets today's script with only the Wi-Fi reload swapped for
// liveBlock; the rollback still restores and reloads.
func TestLiveApplyScript(t *testing.T) {
	ops := []translate.Op{{Key: "wireless.s1.maclist", List: []string{"02:00:00:00:00:01"}}}
	changed := []translate.ACL{{Section: "s1", Policy: "deny", MACs: []string{"02:00:00:00:00:01"}}}
	keep := []string{"02:00:00:00:00:02"}
	_, normal, err := applyScript(ops, time.Minute, "ID4")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(normal, wifiReload+"\n") != 1 {
		t.Fatalf("not one Wi-Fi reload in:\n%s", normal)
	}
	_, live, err := liveApplyScript(ops, changed, keep, time.Minute, "ID4")
	if err != nil {
		t.Fatal(err)
	}
	block, err := liveBlock(changed, keep, "/var/run", wifiReload)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(normal, wifiReload+"\n", block, 1); live != want {
		t.Errorf("live script is not the normal one with the reload swapped:\n%s", live)
	}
	rb := live[strings.Index(live, "<<'RB'"):strings.Index(live, "\nRB\n")]
	if !strings.Contains(rb, "cp -p /etc/config/wireless.pre-unifi-ID4 /etc/config/wireless") || !strings.Contains(rb, "|| true; wifi\n") {
		t.Errorf("rollback no longer restores and reloads:\n%s", rb)
	}
	for _, s := range []string{live, rb[len("<<'RB'\n"):]} {
		c := exec.Command("sh", "-n")
		c.Stdin = strings.NewReader(s)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("sh -n: %v %s\n%s", err, out, s)
		}
	}
}

// wifiProbe output: the radio is "up" for netifd in every case, what differs
// is hostapd's state of the BSSes.
func TestWifiHealthBSS(t *testing.T) {
	status := `{"radio0":{"up":true,"disabled":false,"interfaces":[{"ifname":"phy0-ap0","config":{"mode":"ap"}},{"ifname":"phy0-sta0","config":{"mode":"sta"}}]},` +
		`"radio1":{"up":true,"disabled":false,"interfaces":[{"ifname":"phy1-ap0","config":{"mode":"ap"}}]}}`
	iw := "phy#1\n\tInterface phy1-ap0\n\t\ttype AP\n\t\tchannel 36 (5180 MHz), width: 80 MHz, center1: 5210 MHz\n" +
		"phy#0\n\tInterface phy0-sta0\n\t\ttype managed\n\tInterface phy0-ap0\n\t\ttype AP\n\t\tchannel 6 (2437 MHz), width: 20 MHz, center1: 2437 MHz\n"
	noChan := "phy#1\n\tInterface phy1-ap0\n\t\ttype AP\n\t\ttxpower 20.00 dBm\nphy#0\n\tInterface phy0-ap0\n\t\ttype AP\n\t\tchannel 6 (2437 MHz), width: 20 MHz, center1: 2437 MHz\n"
	probe := func(status, hostapd, iw string) []byte {
		return []byte(status + "\n=== hostapd\n" + hostapd + "=== iw\n" + iw)
	}
	for _, c := range []struct {
		name         string
		out          []byte
		ok, starting bool
		why          string
	}{
		{"all on the air", probe(status, "hostapd.phy0-ap0 ENABLED\nhostapd.phy1-ap0 ENABLED\n", iw), true, false, "2 radios ok"},
		{"radar check", probe(status, "hostapd.phy0-ap0 ENABLED\nhostapd.phy1-ap0 DFS\n", noChan), true, false, "2 radios ok"},
		{"channel selection running", probe(status, "hostapd.phy0-ap0 ENABLED\nhostapd.phy1-ap0 ACS\n", noChan), false, true, "phy1-ap0 (ACS)"},
		{"channel selection failed", probe(status, "hostapd.phy0-ap0 ENABLED\nhostapd.phy1-ap0 DISABLED\n", noChan), false, false, "phy1-ap0 (DISABLED)"},
		{"hostapd gone, no channel", probe(status, "hostapd.phy0-ap0 ENABLED\n", noChan), false, false, "phy1-ap0 (no channel)"},
		{"no hostapd objects, on a channel", probe(status, "", iw), true, false, "2 radios ok"},
		{"no state from hostapd, on a channel", probe(status, "hostapd.phy0-ap0 \nhostapd.phy1-ap0 \n", iw), true, false, "2 radios ok"},
		{"name not known yet", probe(strings.Replace(status, `"ifname":"phy1-ap0",`, "", 1), "hostapd.phy0-ap0 ENABLED\n", iw), false, true, "radio1"},
		{"broken wins over starting", probe(status, "hostapd.phy0-ap0 ACS\nhostapd.phy1-ap0 DISABLED\n", noChan), false, false, "phy1-ap0 (DISABLED)"},
		{"radio down", probe(strings.Replace(status, `"radio1":{"up":true`, `"radio1":{"up":false`, 1), "hostapd.phy0-ap0 ENABLED\n", iw), false, false, "radios not up: [radio1]"},
	} {
		ok, starting, why := wifiHealth(c.out)
		if ok != c.ok || starting != c.starting || !strings.Contains(why, c.why) {
			t.Errorf("%s: ok=%v starting=%v why=%q, want ok=%v starting=%v why with %q", c.name, ok, starting, why, c.ok, c.starting, c.why)
		}
	}
}
