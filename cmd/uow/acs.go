// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"encoding/json"
	"log"
	"os"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// A radio on "auto" that hostapd's channel selection leaves without a
// channel gets one from the daemon: see internal/openwrt/acs.go.

// acsWait is how long a radio must stay without a channel before the daemon
// chooses one. A radio is in that state for some seconds every time it
// starts.
const acsWait = time.Minute

var (
	acsPicks = map[string]translate.AutoChannel{} // by UCI wifi-device; kept in the state directory
	acsPath  string
	acsSince = map[string]time.Time{} // first look that found the radio without a channel
	acsNone  = map[string]bool{}      // "no channel fits" already logged
)

func loadACS(path string) {
	acsPath = path
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &acsPicks)
	}
}

// acsRetry: an apply is being tried again with a channel from the daemon
// (syncAP), so a second failure is not tried a third time.
var acsRetry bool

// checkACS looks for radios whose automatic channel selection has failed and
// chooses a channel for those that stayed that way since an earlier look. It
// returns true when it chose one, so the caller plans again.
func checkACS(ap *openwrt.Client) bool { return pickACS(ap, acsWait) }

// pickACS is checkACS with the time a radio must have been without a channel;
// 0 = choose at once (after an apply whose health check has already waited).
func pickACS(ap *openwrt.Client, wait time.Duration) bool {
	fails, err := ap.ACSFailures()
	if err != nil {
		return false
	}
	now, chosen := time.Now(), false
	failing := map[string]bool{}
	for _, f := range fails {
		failing[f.Section] = true
		first, seen := acsSince[f.Section]
		if !seen {
			acsSince[f.Section], first = now, now
			if wait > 0 {
				continue
			}
		}
		if now.Sub(first) < wait {
			continue
		}
		ch := openwrt.PickChannel(f, scans.bssPerChannel())
		if ch == 0 {
			if !acsNone[f.Section] {
				acsNone[f.Section] = true
				log.Printf("AUTO CHANNEL: %s (%s, %s) has been without a channel for %s (hostapd's channel selection failed) and no channel fits that width without radar detection: left as it is", f.Section, f.Band, f.HTMode, now.Sub(first).Round(time.Second))
			}
			continue
		}
		log.Printf("AUTO CHANNEL: %s (%s, %s) has been without a channel for %s (hostapd's channel selection failed): the daemon takes channel %d for it", f.Section, f.Band, f.HTMode, now.Sub(first).Round(time.Second), ch)
		acsPicks[f.Section] = translate.AutoChannel{Channel: ch, HTMode: f.HTMode}
		delete(acsSince, f.Section)
		chosen = true
	}
	for sec := range acsSince {
		if !failing[sec] {
			delete(acsSince, sec)
			delete(acsNone, sec)
		}
	}
	if chosen && acsPath != "" {
		if b, err := json.Marshal(acsPicks); err == nil {
			os.WriteFile(acsPath, b, 0o600)
		}
	}
	return chosen
}
