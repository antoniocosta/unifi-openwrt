// SPDX-License-Identifier: GPL-2.0-or-later

package main

import "testing"

func TestAppliedMsg(t *testing.T) {
	for how, want := range map[string]string{
		"reload": "apply ID: committed, reloading; checking (rollback in 5 min unless confirmed)",
		"live":   "apply ID: committed, MAC lists changed in hostapd without a Wi-Fi reload; checking (rollback in 5 min unless confirmed)",
		"reload: no hostapd control socket for phy0-ap0": "apply ID: committed, live MAC list update not possible (no hostapd control socket for phy0-ap0): Wi-Fi reloaded; checking (rollback in 5 min unless confirmed)",
	} {
		if got := appliedMsg("ID", how); got != want {
			t.Errorf("%q:\n got %s\nwant %s", how, got, want)
		}
	}
}
