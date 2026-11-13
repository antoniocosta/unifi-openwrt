// SPDX-License-Identifier: GPL-2.0-or-later

package models

import "testing"

func TestCatalog(t *testing.T) {
	m, ok := Lookup("u7pg2")
	if !ok || m.Display != "UniFi AP-AC-Pro" || m.Gen != 5 {
		t.Fatalf("%+v", m)
	}
	p := Profile(m)
	if p.Model != "U7PG2" || !p.Radios["na"].Is11ac || p.Radios["ng"].Is11ac {
		t.Fatalf("profile %+v", p)
	}
}

func TestSuggest(t *testing.T) {
	// the OnHub: 2.4 + 5 GHz, 3x3, Wi-Fi 5
	s := Suggest(AP{Bands: []string{"ng", "na"}, NSS: 3, Gen: 5})
	if s[0].Code != "U7MP" {
		t.Logf("top: %v", s[:3])
	}
	if s[0].Gen != 5 || s[0].Bands() != "ng+na" {
		t.Fatalf("top suggestion %v", s[0])
	}
	// a Wi-Fi 6 tri-band AP
	if s := Suggest(AP{Bands: []string{"ng", "na", "6e"}, NSS: 2, Gen: 6}); s[0].Gen != 6 || s[0].Bands() != "ng+na+6e" {
		t.Fatalf("6E top %v", s[0])
	}
}
