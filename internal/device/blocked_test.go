// SPDX-License-Identifier: GPL-2.0-or-later

package device

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBlockedSta(t *testing.T) {
	x := &Session{StateDir: t.TempDir()}
	o, err := x.Handle([]byte(`{"_type":"setparam","blocked_sta":"AA:BB:CC:00:11:22\n02:00:00:00:00:01\nbogus\naa:bb:cc:00:11:22"}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"02:00:00:00:00:01", "aa:bb:cc:00:11:22"}
	if !reflect.DeepEqual(x.S.Blocked, want) || !reflect.DeepEqual(o.Actions, []string{"block"}) {
		t.Fatalf("blocked %v actions %v", x.S.Blocked, o.Actions)
	}
	// same list again: no action
	o, _ = x.Handle([]byte(`{"_type":"setparam","blocked_sta":"02:00:00:00:00:01\naa:bb:cc:00:11:22"}`), time.Now())
	if len(o.Actions) != 0 {
		t.Fatalf("unchanged list gave actions %v", o.Actions)
	}
	// setparam without blocked_sta leaves the list alone
	x.Handle([]byte(`{"_type":"setparam","mgmt_cfg":"led_enabled=true"}`), time.Now())
	if len(x.S.Blocked) != 2 {
		t.Fatalf("list lost: %v", x.S.Blocked)
	}
	// unblock one via cmd, then empty string clears
	x.Handle([]byte(`{"_type":"cmd","cmd":"unblock-sta","mac":"AA:BB:CC:00:11:22"}`), time.Now())
	if !reflect.DeepEqual(x.S.Blocked, []string{"02:00:00:00:00:01"}) {
		t.Fatalf("after unblock: %v", x.S.Blocked)
	}
	o, _ = x.Handle([]byte(`{"_type":"setparam","blocked_sta":""}`), time.Now())
	if len(x.S.Blocked) != 0 || !reflect.DeepEqual(o.Actions, []string{"unblock:02:00:00:00:00:01", "block"}) {
		t.Fatalf("clear: %v %v", x.S.Blocked, o.Actions)
	}
}

func TestKickSta(t *testing.T) {
	x := &Session{StateDir: t.TempDir()}
	o, _ := x.Handle([]byte(`{"_type":"cmd","cmd":"kick-sta","mac":"02:00:00:00:00:01"}`), time.Now())
	if !reflect.DeepEqual(o.Actions, []string{"kick:02:00:00:00:00:01"}) {
		t.Fatalf("actions %v", o.Actions)
	}
	o, _ = x.Handle([]byte(`{"_type":"cmd","cmd":"kick-sta","mac":"02:00:00:00:00:01; reboot"}`), time.Now())
	if len(o.Actions) != 0 {
		t.Fatalf("bad mac accepted: %v", o.Actions)
	}
}

func TestGuestAuth(t *testing.T) {
	x := &Session{StateDir: t.TempDir()}
	now := time.Unix(1_800_000_000, 0)
	o, _ := x.Handle([]byte(`{"_type":"cmd","cmd":"authorize-guest","mac":"02:00:00:00:00:A2","minutes":60}`), now)
	if !reflect.DeepEqual(o.Actions, []string{"guest-auth:02:00:00:00:00:a2"}) || x.S.Guests["02:00:00:00:00:a2"] != now.Add(time.Hour).Unix() {
		t.Fatalf("auth: %v %v", o.Actions, x.S.Guests)
	}
	o, _ = x.Handle([]byte(`{"_type":"cmd","cmd":"unauthorize-guest","mac":"02:00:00:00:00:a2"}`), now)
	if !reflect.DeepEqual(o.Actions, []string{"guest-unauth:02:00:00:00:00:a2"}) || len(x.S.Guests) != 0 {
		t.Fatalf("unauth: %v %v", o.Actions, x.S.Guests)
	}
}

func TestPruneHistory(t *testing.T) {
	d := t.TempDir()
	for i := 0; i < 25; i++ {
		os.WriteFile(filepath.Join(d, fmt.Sprintf("20261112-%06d-x.txt", i)), nil, 0o600)
	}
	pruneHistory(d, 20)
	es, _ := os.ReadDir(d)
	if len(es) != 20 || es[0].Name() != "20261112-000005-x.txt" {
		t.Fatalf("%d left, first %s", len(es), es[0].Name())
	}
}
