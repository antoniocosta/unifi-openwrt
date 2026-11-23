// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// A radio on "auto" leaves the channel to hostapd (ACS). hostapd can fail at
// it and then the radio's WLANs stay off, with netifd still reporting the
// radio as up. Seen on mwl8k (Linksys E4200 v2): with HT40 on 5 GHz hostapd
// picked channel 48 every time, which has no upper neighbour on that radio,
// and gave up ("ACS: Could not (err: -3) select hw_mode for freq=5240
// channel=48"); a retry ends the same way. With a wireless uplink on the
// radio the channel is the parent's and ACS never runs, so it shows once the
// AP is on a cable.
//
// The daemon looks for such a radio once a minute and, when it stays that
// way, picks a channel itself (PickChannel). The plan then asks for that
// channel instead of "auto" (translate.Features.AutoChannel).

// acsProbe prints, for every enabled wifi-device on channel auto whose
// interfaces are all AP interfaces without a channel, a "UOW-ACS section band
// htmode" line followed by the channels of its phy (iw phy ... channels).
const acsProbe = `st=$(ubus call network.wireless status 2>/dev/null) || exit 0
for dev in $(uci show wireless 2>/dev/null | sed -n "s/^wireless\.\([A-Za-z0-9_]*\)=wifi-device$/\1/p"); do
  [ "$(uci -q get wireless.$dev.channel)" = auto ] || continue
  [ "$(uci -q get wireless.$dev.disabled)" = 1 ] && continue
  ifs=$(echo "$st" | jsonfilter -e "@.$dev.interfaces[*].ifname" 2>/dev/null)
  [ -n "$ifs" ] || continue
  bad=1; phy=
  for i in $ifs; do
    inf=$(iw dev "$i" info 2>/dev/null) || { bad=0; break; }
    case "$inf" in *"type AP"*) ;; *) bad=0;; esac
    case "$inf" in *channel*) bad=0;; esac
    p=$(cat /sys/class/net/$i/phy80211/name 2>/dev/null); [ -n "$p" ] && phy=$p
  done
  [ $bad = 1 ] && [ -n "$phy" ] || continue
  echo "UOW-ACS $dev $(uci -q get wireless.$dev.band) $(uci -q get wireless.$dev.htmode)"
  iw phy "$phy" channels 2>/dev/null
done
true`

// ACSFailure is a radio on channel auto whose AP interfaces are on no
// channel.
type ACSFailure struct {
	Section string // UCI wifi-device
	Band    string // UCI band: 2g, 5g, 6g
	HTMode  string // UCI htmode
	// Channels the radio may start an AP on right away, in its band: not
	// disabled, no radar detection, no "No IR".
	Channels []int
}

// ACSFailures reports the radios whose automatic channel selection has not
// produced a channel at this moment. A radio is also in this state for some
// seconds while it starts, so the caller acts only on one that stays in it.
func (cl *Client) ACSFailures() ([]ACSFailure, error) {
	out, err := cl.Run(acsProbe)
	if err != nil {
		return nil, err
	}
	return parseACSProbe(string(out)), nil
}

var iwChannel = regexp.MustCompile(`^\s*\* (\d+) MHz \[(\d+)\]`)

func parseACSProbe(out string) []ACSFailure {
	var res []ACSFailure
	type ch struct {
		n  int
		ok bool
	}
	var chans []ch
	flush := func() {
		if len(res) == 0 {
			return
		}
		f := &res[len(res)-1]
		for _, c := range chans {
			if c.ok {
				f.Channels = append(f.Channels, c.n)
			}
		}
		sort.Ints(f.Channels)
		chans = nil
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "UOW-ACS "); ok {
			flush()
			w := strings.Fields(rest)
			if len(w) < 2 || !sectionOK.MatchString(w[0]) {
				res = append(res, ACSFailure{}) // takes the channel lines that follow; dropped below
				continue
			}
			f := ACSFailure{Section: w[0], Band: w[1]}
			if len(w) > 2 {
				f.HTMode = w[2]
			}
			res = append(res, f)
			continue
		}
		if len(res) == 0 {
			continue
		}
		if m := iwChannel.FindStringSubmatch(line); m != nil {
			freq, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[2])
			band := "5g"
			switch {
			case freq < 3000:
				band = "2g"
			case freq > 5925:
				band = "6g"
			}
			chans = append(chans, ch{n, band == res[len(res)-1].Band && !strings.Contains(line, "disabled")})
			continue
		}
		if len(chans) > 0 && (strings.Contains(line, "Radar detection") || strings.Contains(line, "No IR")) {
			chans[len(chans)-1].ok = false
		}
	}
	flush()
	var good []ACSFailure
	for _, f := range res {
		if f.Section != "" {
			good = append(good, f)
		}
	}
	return good
}

// htWidth is the channel width in MHz an htmode asks for (HT40+, VHT80,
// HE160, EHT320...).
func htWidth(htmode string) int {
	w := 0
	for _, c := range htmode {
		if c >= '0' && c <= '9' {
			w = w*10 + int(c-'0')
		} else if w > 0 {
			break
		}
	}
	switch {
	case w >= 160:
		return 160
	case w >= 80:
		return 80
	case w >= 40:
		return 40
	}
	return 20
}

// block5 lists the channels of the width-MHz block that holds channel c in
// the 5 GHz channel plan (36-144 and 149-177, each in steps of 20 MHz).
func block5(c, width int) []int {
	base := 36
	if c >= 149 {
		base = 149
	}
	n := width / 20
	start := base + 4*((c-base)/4/n*n)
	out := make([]int, n)
	for i := range out {
		out[i] = start + 4*i
	}
	return out
}

// PickChannel chooses a channel for a radio whose automatic selection failed:
// among the channels the radio can use at once for the configured width, the
// one whose block has the fewest networks in seen (BSSes per channel, from
// the daemon's scans); the lowest of equals. On 2.4 GHz only 1, 6 and 11 are
// candidates. 0 = no channel fits (the radio stays as it is). The 6 GHz band
// is left alone.
func PickChannel(f ACSFailure, seen map[int]int) int {
	ok := map[int]bool{}
	for _, c := range f.Channels {
		ok[c] = true
	}
	best, bestScore := 0, 0
	try := func(primary int, block []int) {
		score := 0
		for _, c := range block {
			score += seen[c]
		}
		if best == 0 || score < bestScore || score == bestScore && primary < best {
			best, bestScore = primary, score
		}
	}
	switch f.Band {
	case "2g":
		for _, c := range []int{1, 6, 11} {
			if ok[c] {
				try(c, []int{c - 2, c - 1, c, c + 1, c + 2})
			}
		}
	case "5g":
		width := htWidth(f.HTMode)
		done := map[int]bool{}
		for _, c := range f.Channels {
			if c < 36 || (c-36)%4 != 0 && (c < 149 || (c-149)%4 != 0) {
				continue
			}
			b := block5(c, width)
			if done[b[0]] {
				continue
			}
			done[b[0]] = true
			whole := true
			for _, x := range b {
				whole = whole && ok[x]
			}
			if whole {
				try(b[0], b)
			}
		}
	}
	return best
}
