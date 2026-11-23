// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"reflect"
	"testing"
)

// The probe's output for a dual-band chip whose 5 GHz device failed: the phy
// lists both bands, a disabled channel and channels with radar detection.
const acsProbeOut = `UOW-ACS radio1 5g HT40
Band 1:
	* 2412 MHz [1]
	  Maximum TX power: 30.0 dBm
	  Channel widths: 20MHz HT40+
	* 2437 MHz [6]
	  Maximum TX power: 30.0 dBm
Band 2:
	* 5180 MHz [36]
	  Maximum TX power: 27.0 dBm
	  Channel widths: 20MHz HT40+
	* 5200 MHz [40]
	  Maximum TX power: 27.0 dBm
	* 5220 MHz [44]
	* 5240 MHz [48]
	* 5260 MHz [52] (disabled)
	* 5500 MHz [100]
	  Maximum TX power: 27.0 dBm
	  Radar detection
	  Channel widths: 20MHz HT40+
	* 5745 MHz [149]
	* 5765 MHz [153]
	* 5785 MHz [157]
	* 5805 MHz [161]
	* 5825 MHz [165]
	* 5845 MHz [169]
	  No IR
UOW-ACS bad;name 5g HT20
	* 5180 MHz [36]
UOW-ACS radio0 2g
	* 2412 MHz [1]
	* 2437 MHz [6]
	* 2462 MHz [11]
	* 2484 MHz [14] (disabled)
`

func TestParseACSProbe(t *testing.T) {
	got := parseACSProbe(acsProbeOut)
	want := []ACSFailure{
		{Section: "radio1", Band: "5g", HTMode: "HT40", Channels: []int{36, 40, 44, 48, 149, 153, 157, 161, 165}},
		{Section: "radio0", Band: "2g", Channels: []int{1, 6, 11}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if got := parseACSProbe(""); len(got) != 0 {
		t.Errorf("nothing failed: %+v", got)
	}
}

func TestPickChannel(t *testing.T) {
	five := []int{36, 40, 44, 48, 149, 153, 157, 161, 165}
	for _, c := range []struct {
		name string
		f    ACSFailure
		seen map[int]int
		want int
	}{
		{"40 MHz, nothing seen: the lowest pair", ACSFailure{Band: "5g", HTMode: "HT40", Channels: five}, nil, 36},
		{"40 MHz: the pair with the fewest networks", ACSFailure{Band: "5g", HTMode: "HT40", Channels: five},
			map[int]int{36: 2, 40: 1, 44: 1, 48: 3, 149: 0, 153: 2, 157: 1}, 157},
		{"40 MHz: 165 has no partner and is never taken", ACSFailure{Band: "5g", HTMode: "HT40", Channels: five},
			map[int]int{36: 9, 44: 9, 149: 9, 157: 9}, 36},
		{"20 MHz: any channel, the emptiest", ACSFailure{Band: "5g", HTMode: "HT20", Channels: five},
			map[int]int{36: 1, 40: 1, 44: 1, 48: 1, 149: 1, 153: 1, 157: 1, 161: 1}, 165},
		{"80 MHz: whole blocks only", ACSFailure{Band: "5g", HTMode: "VHT80", Channels: five}, map[int]int{40: 1}, 149},
		{"80 MHz: a block with a hole is out", ACSFailure{Band: "5g", HTMode: "HE80", Channels: []int{36, 40, 44, 149, 153, 157, 161}}, nil, 149},
		{"160 MHz without the radar channels: nothing fits", ACSFailure{Band: "5g", HTMode: "VHT160", Channels: five}, nil, 0},
		{"2.4 GHz: 1, 6 or 11", ACSFailure{Band: "2g", HTMode: "HT20", Channels: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}},
			map[int]int{1: 3, 2: 1, 6: 2, 9: 1, 11: 1, 13: 4}, 6},
		{"2.4 GHz: neighbours on 12 and 13 count against 11", ACSFailure{Band: "2g", Channels: []int{1, 6, 11}},
			map[int]int{1: 2, 6: 2, 13: 1}, 11},
		{"6 GHz is left alone", ACSFailure{Band: "6g", HTMode: "HE80", Channels: []int{1, 5, 9}}, nil, 0},
		{"no channel at all", ACSFailure{Band: "5g", HTMode: "HT40"}, nil, 0},
	} {
		if got := PickChannel(c.f, c.seen); got != c.want {
			t.Errorf("%s: channel %d, want %d", c.name, got, c.want)
		}
	}
}

func TestBlock5(t *testing.T) {
	for _, c := range []struct {
		ch, width int
		want      []int
	}{
		{48, 40, []int{44, 48}},
		{36, 20, []int{36}},
		{60, 80, []int{52, 56, 60, 64}},
		{161, 40, []int{157, 161}},
		{165, 40, []int{165, 169}},
		{112, 160, []int{100, 104, 108, 112, 116, 120, 124, 128}},
	} {
		if got := block5(c.ch, c.width); !reflect.DeepEqual(got, c.want) {
			t.Errorf("block5(%d, %d) = %v, want %v", c.ch, c.width, got, c.want)
		}
	}
}
