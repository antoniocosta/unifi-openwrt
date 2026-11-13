// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os/exec"
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
