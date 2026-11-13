// SPDX-License-Identifier: GPL-2.0-or-later

// Package translate turns a UniFi AP system_cfg into OpenWrt UCI wireless
// settings and diffs them against the AP's current UCI.
//
// Log-only first: Plan returns the changes; nothing here applies them.
// Secrets (passphrases) are compared by hash and never printed.
package translate

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Cfg is system_cfg as key -> value.
type Cfg map[string]string

func ParseCfg(text string) Cfg {
	c := Cfg{}
	for _, line := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "#") {
			c[strings.TrimSpace(k)] = v
		}
	}
	return c
}

// UCI is `uci show wireless` as key -> value (quotes stripped).
type UCI map[string]string

func ParseUCI(text string) UCI {
	u := UCI{}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.Count(v, "'") > 2 {
			u[k] = v // a list: keep `'a' 'b'` as printed
		} else {
			u[k] = strings.Trim(v, "'")
		}
	}
	return u
}

// Op is one UCI change.
type Op struct {
	List   []string // set a list option: replaces all values (Value unused)
	Del    bool
	Key    string // e.g. wireless.radio1.htmode
	Value  string
	Old    string
	Secret bool
}

func (o Op) String() string {
	if o.List != nil {
		return fmt.Sprintf("uci set list %s=%q   (was %q)", o.Key, o.List, o.Old)
	}
	if o.Del {
		return fmt.Sprintf("uci delete %s   (was %s)", o.Key, mask(o.Old, o.Secret))
	}
	if o.Old == "" {
		return fmt.Sprintf("uci set %s=%s   (new)", o.Key, mask(o.Value, o.Secret))
	}
	return fmt.Sprintf("uci set %s=%s   (was %s)", o.Key, mask(o.Value, o.Secret), mask(o.Old, o.Secret))
}

func mask(v string, secret bool) string {
	if !secret || v == "" {
		return "'" + v + "'"
	}
	h := sha256.Sum256([]byte(v))
	return fmt.Sprintf("<secret %x…>", h[:3])
}

// Result is the plan plus what was deliberately not translated.
type Result struct {
	Ops       []Op
	Notes     []string   // decisions and clamps worth reading
	Skipped   []string   // system_cfg sections not handled (yet)
	Schedules []Schedule // WLAN broadcast schedules (the AP enforces them)
}

// handled lists system_cfg top-level sections the translator reads.
var handled = map[string]bool{"qos": true, "bandsteering": true, "radio": true, "aaa": true, "wireless": true, "system": true, "locale": true, "ntpclient": true, "macacl": true, "users": true, "netconf": true, "dhcpc": true, "route": true, "resolv": true, "syslog": true, "ledbar": true, "snmp": true, "stamgr": true, "vlan": true, "bridge": true}

// Features is what the AP's software supports (see openwrt.Client.Features).
type Features struct {
	APAddr  string // address the daemon reaches the AP on (static IPs must keep it)
	K, V, R bool
	// Phys: the radio names we report to the controller and their band
	// ("wifi0" -> "2g"). When set, system_cfg radios with other names are
	// ignored (the controller can remember radios that do not exist).
	Phys map[string]string
}

// Plan computes the UCI changes that make the AP match system_cfg. Settings
// the AP cannot honour are clamped (left off) and noted.
func Plan(cfg Cfg, cur UCI, f Features) Result {
	var r Result
	radios := apRadios(cur) // band -> uci radio section
	phyBand := map[string]string{}

	// --- radios ---
	for _, n := range indices(cfg, "radio") {
		p := "radio." + n + "."
		phy := cfg[p+"phyname"]
		mode := cfg[p+"ieee_mode"]
		band := "5g"
		if strings.Contains(mode, "ng") || phy == "wifi0" && !strings.Contains(mode, "na") {
			band = "2g"
		}
		if f.Phys != nil {
			b, ok := f.Phys[phy]
			if !ok {
				r.Notes = append(r.Notes, fmt.Sprintf("radio.%s (%s): not a radio this AP reports, ignored", n, phy))
				continue
			}
			band = b
		}
		phyBand[phy] = band
		sec, ok := radios[band]
		if !ok {
			r.Notes = append(r.Notes, fmt.Sprintf("radio.%s (%s, %s): no %s radio on this AP, skipped", n, phy, mode, band))
			continue
		}
		k := "wireless." + sec + "."
		want := map[string]string{}
		if cfg[p+"status"] == "disabled" {
			want["disabled"] = "1"
		} else {
			want["disabled"] = "0"
		}
		if ch := cfg[p+"channel"]; ch != "" {
			want["channel"] = ch // "auto" or a number
		}
		if ht := htmode(mode, cur[k+"htmode"]); ht != "" {
			want["htmode"] = ht
		}
		if cc := countryCode(cfg[p+"countrycode"], cfg["radio.countrycode"]); cc != "" {
			want["country"] = cc
		}
		switch tp := cfg[p+"txpower"]; {
		case tp == "auto" || tp == "":
			if cur[k+"txpower"] != "" {
				r.Ops = append(r.Ops, Op{Del: true, Key: k + "txpower", Old: cur[k+"txpower"]})
			}
		default:
			if _, err := strconv.Atoi(tp); err == nil {
				want["txpower"] = tp
			}
		}
		r.Ops = append(r.Ops, diff(k, want, cur, nil)...)
	}

	// --- WLANs: aaa.N (security) + wireless.N (vap) per radio ---
	type iface struct{ ssid, device string }
	wanted := map[iface]bool{}
	vlanNet, _ := vlanNetworks(cfg) // VAP devname -> network of its VLAN (vlan.go)
	radioRates := map[string][]wlanRate{}
	for _, n := range indices(cfg, "wireless") {
		w, a := "wireless."+n+".", "aaa."+n+"."
		if cfg[w+"usage"] == "uplink" || cfg[w+"vwire"] == "enabled" || cfg[w+"vport"] == "enabled" {
			r.Notes = append(r.Notes, fmt.Sprintf("wireless.%s: mesh/uplink vap, not translated (serving mesh children is not supported; the wireless uplink is the mesh_uplink watcher)", n))
			continue
		}
		if cfg[w+"element_adopt"] == "enabled" {
			// UniFi's hidden adoption SSID (new UniFi devices join it to be
			// adopted over Wi-Fi). Translated like any WLAN; it is hidden.
			r.Notes = append(r.Notes, fmt.Sprintf("wireless.%s: element-adopt hidden SSID %q", n, cfg[w+"ssid"]))
		}
		band, known := phyBand[cfg[w+"parent"]]
		sec, ok := radios[band]
		if !known || !ok {
			r.Notes = append(r.Notes, fmt.Sprintf("wireless.%s (%q on %s): radio not on this AP, ignored", n, cfg[w+"ssid"], cfg[w+"parent"]))
			continue
		}
		ssid := cfg[w+"ssid"]
		if ssid == "" {
			ssid = cfg[a+"ssid"]
		}
		wanted[iface{ssid, sec}] = true
		ifs := findIface(cur, sec, ssid)
		if ifs == "" {
			ifs = fmt.Sprintf("unifi_%s_%s", sec, n)
			r.Notes = append(r.Notes, fmt.Sprintf("new wifi-iface %s for %q on %s", ifs, ssid, sec))
			r.Ops = append(r.Ops, Op{Key: "wireless." + ifs, Value: "wifi-iface"})
		}
		k := "wireless." + ifs + "."
		sched := wlanSchedule(cfg, w, ifs)
		want := map[string]string{
			"device": sec, "mode": "ap", "network": "lan", "ssid": ssid,
			"disabled": boolStr(cfg[w+"status"] == "disabled" || cfg[a+"status"] == "disabled"),
			"hidden":   boolStr(cfg[w+"hide_ssid"] == "true" || cfg[a+"hide_ssid"] == "true"),
			"isolate":  boolStr(cfg[w+"l2_isolation"] == "enabled"),
			"uapsd":    boolStr(cfg[w+"uapsd"] == "enabled"), // OpenWrt default 1, UniFi default off
		}
		if n := vlanNet[cfg[w+"devname"]]; n != "" {
			want["network"] = n
		}
		if sched != nil && want["disabled"] == "0" {
			// the AP's schedule script owns "disabled" for this WLAN
			delete(want, "disabled")
			r.Schedules = append(r.Schedules, *sched)
		}
		enc, note := encryption(cfg, a)
		want["encryption"] = enc
		if note != "" && !strings.Contains(cur[k+"encryption"], "sae") && strings.Contains(note, "WPA3") {
			note = "" // nothing to lose: the interface had no WPA3
		}
		if note != "" {
			r.Notes = append(r.Notes, fmt.Sprintf("%s (%q on %s): %s", ifs, ssid, sec, note))
		}
		if enc != "none" {
			want["ieee80211w"] = pmf(cfg[a+"pmf.status"], cfg[a+"pmf.mode"])
		}
		for _, x := range []struct {
			opt, what string
			on, ok    bool
		}{
			{"ieee80211r", "802.11r (fast roaming)", cfg[a+"ft.status"] == "enabled", f.R},
			{"ieee80211k", "802.11k", cfg[a+"11k.status"] == "enabled", f.K},
			{"bss_transition", "802.11v (BSS transition)", cfg[a+"bss_transition"] == "enabled", f.V},
		} {
			if x.on && !x.ok {
				r.Notes = append(r.Notes, fmt.Sprintf("%s: %s requested but this AP's hostapd lacks it: left off (needs full wpad)", ifs, x.what))
				x.on = false
			}
			want[x.opt] = boolStr(x.on)
		}
		if d := cfg[w+"dtim_period"]; d != "" {
			want["dtim_period"] = d
		}
		// min data rate: OpenWrt applies rates per radio only (the iface
		// options are not passed to hostapd); collect per radio
		if cfg[w+"status"] != "disabled" && cfg[a+"status"] != "disabled" {
			sup, basic := wlanRates(cfg, w, band)
			radioRates[sec] = append(radioRates[sec], wlanRate{ssid, sup, basic})
		}
		for _, o := range []string{"supported_rates", "basic_rate"} {
			if old, has := cur[k+o]; has {
				r.Ops = append(r.Ops, Op{Del: true, Key: k + o, Old: old})
			}
		}
		policy, macs := macACL(cfg, cfg[w+"devname"])
		if policy != "disable" {
			want["macfilter"] = policy
		}
		r.Ops = append(r.Ops, diff(k, want, cur, nil)...)
		if policy == "disable" {
			// OpenWrt accepts only macfilter allow|deny ("disable" breaks the
			// radio): no filter = no macfilter option.
			for _, o := range []string{"macfilter", "maclist"} {
				if old, ok := cur[k+o]; ok {
					r.Ops = append(r.Ops, Op{Del: true, Key: k + o, Old: old})
				}
			}
		} else if !listEq(cur[k+"maclist"], macs) {
			r.Ops = append(r.Ops, Op{Key: k + "maclist", List: macs, Old: cur[k+"maclist"]})
		}
		// Key management as a UAP advertises it. OpenWrt adds WPA-PSK-SHA256
		// whenever PMF is optional; a UAP does not (it advertises "PSK SAE"),
		// and some older Macs ignore a BSS that offers it: seen in testing,
		// a MacBook never tried the OpenWrt AP even at -30 dBm (docs/internals.md).
		var bssOpts []string
		if km := keyMgmt(cfg, a, enc, want["ieee80211r"] == "1"); km != "" {
			bssOpts = []string{"wpa_key_mgmt=" + km}
		}
		if !listEq(cur[k+"hostapd_bss_options"], bssOpts) {
			if len(bssOpts) == 0 {
				r.Ops = append(r.Ops, Op{Del: true, Key: k + "hostapd_bss_options", Old: cur[k+"hostapd_bss_options"]})
			} else {
				r.Ops = append(r.Ops, Op{Key: k + "hostapd_bss_options", List: bssOpts, Old: cur[k+"hostapd_bss_options"]})
			}
		}
		if psk := cfg[a+"wpa.psk"]; psk != "" && enc != "none" {
			if cur[k+"key"] != psk {
				r.Ops = append(r.Ops, Op{Key: k + "key", Value: psk, Old: cur[k+"key"], Secret: true})
			}
		}
	}
	r.Ops = append(r.Ops, radioRateOps(radioRates, cur, &r)...)
	r.Ops = append(r.Ops, planStamgr(cfg, cur, &r)...)
	r.Ops = append(r.Ops, planOutdoor(cfg, cur, &r)...)

	// AP interfaces the controller no longer wants are disabled, not deleted.
	for key, v := range cur {
		if v != "wifi-iface" {
			continue
		}
		s := strings.TrimPrefix(key, "wireless.")
		if cur[key+".mode"] != "ap" {
			continue
		}
		if !wanted[iface{cur[key+".ssid"], cur[key+".device"]}] && cur[key+".disabled"] != "1" {
			r.Ops = append(r.Ops, Op{Key: key + ".disabled", Value: "1", Old: cur[key+".disabled"]})
			r.Notes = append(r.Notes, fmt.Sprintf("%s (%q) is not in the controller's config: would be disabled", s, cur[key+".ssid"]))
		}
	}

	r.Ops = append(r.Ops, planSystem(cfg, cur, &r)...)
	r.Ops = append(r.Ops, planNetwork(cfg, cur, f.APAddr, &r)...)
	r.Ops = append(r.Ops, planVLAN(cfg, cur, &r)...)

	seen := map[string]bool{}
	for k := range cfg {
		top, _, _ := strings.Cut(k, ".")
		if !handled[top] && !seen[top] {
			seen[top] = true
			r.Skipped = append(r.Skipped, top)
		}
	}
	sort.Strings(r.Skipped)
	sort.SliceStable(r.Ops, func(i, j int) bool { return r.Ops[i].Key < r.Ops[j].Key })
	return r
}

// defaultOn: wifi-iface boolean options that OpenWrt treats as 1 when absent.
var defaultOn = map[string]bool{"uapsd": true, "wmm": true}

func diff(prefix string, want map[string]string, cur UCI, _ []Op) []Op {
	var ops []Op
	for opt, v := range want {
		k := prefix + opt
		old, has := cur[k]
		if has && old == v {
			continue
		}
		// UCI defaults: an absent boolean option means 0 (except the few
		// OpenWrt defaults to 1).
		if !has && v == "0" && opt != "disabled" && !defaultOn[opt] {
			continue
		}
		if !has && opt == "disabled" && v == "0" {
			continue
		}
		ops = append(ops, Op{Key: k, Value: v, Old: old})
	}
	return ops
}

// apRadios maps band ("2g", "5g") to the first wifi-device of that band.
// A second radio in the same band (the OnHub's radio2) is left alone.
func apRadios(cur UCI) map[string]string {
	var secs []string
	for k, v := range cur {
		if v == "wifi-device" {
			secs = append(secs, strings.TrimPrefix(k, "wireless."))
		}
	}
	sort.Strings(secs)
	out := map[string]string{}
	for _, s := range secs {
		b := cur["wireless."+s+".band"]
		if _, ok := out[b]; !ok && b != "" {
			out[b] = s
		}
	}
	return out
}

func findIface(cur UCI, device, ssid string) string {
	var keys []string
	for k, v := range cur {
		if v == "wifi-iface" && cur[k+".device"] == device && cur[k+".ssid"] == ssid && cur[k+".mode"] == "ap" {
			keys = append(keys, strings.TrimPrefix(k, "wireless."))
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

func indices(cfg Cfg, top string) []string {
	set := map[string]bool{}
	for k := range cfg {
		rest, ok := strings.CutPrefix(k, top+".")
		if !ok {
			continue
		}
		if n, _, ok := strings.Cut(rest, "."); ok {
			if _, err := strconv.Atoi(n); err == nil {
				set[n] = true
			}
		}
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { a, _ := strconv.Atoi(out[i]); b, _ := strconv.Atoi(out[j]); return a < b })
	return out
}

// htmode maps UniFi ieee_mode (11nght20, 11naht40, 11acvht80, ...) to
// OpenWrt htmode. On a 5 GHz radio that already runs VHT, n-mode widths
// become VHT of the same width (the UniFi names a U7PG2's AC radio "na").
func htmode(mode, cur string) string {
	if mode == "" {
		return ""
	}
	width := "20"
	for _, w := range []string{"160", "80", "40"} {
		if strings.HasSuffix(mode, w) {
			width = w
			break
		}
	}
	switch {
	case strings.Contains(mode, "he"):
		return "HE" + width
	case strings.Contains(mode, "vht"), strings.Contains(mode, "na") && strings.HasPrefix(cur, "VHT"):
		return "VHT" + width
	default:
		return "HT" + width
	}
}

func countryCode(per, global string) string {
	c := per
	if c == "" {
		c = global
	}
	codes := map[string]string{"840": "US", "528": "NL", "276": "DE", "826": "GB", "250": "FR", "56": "BE", "124": "CA", "36": "AU"}
	return codes[c]
}

// encryption maps aaa.N.wpa* to an OpenWrt encryption value.
func encryption(cfg Cfg, a string) (string, string) {
	if cfg[a+"wpa"] == "" && cfg[a+"wpa.psk"] == "" {
		return "none", ""
	}
	var mgmt []string
	for i := 1; i <= 4; i++ {
		if m := cfg[fmt.Sprintf("%swpa.key.%d.mgmt", a, i)]; m != "" {
			mgmt = append(mgmt, strings.Fields(m)...)
		}
	}
	has := func(s string) bool {
		for _, m := range mgmt {
			if m == s {
				return true
			}
		}
		return false
	}
	psk, sae := has("WPA-PSK") || has("WPA-PSK-SHA256"), has("SAE")
	// UniFi lists only SAE for a WPA2/WPA3 network and marks the mix with
	// wpa3.transition (seen live on Network 10.6).
	if sae && cfg[a+"wpa3.transition"] == "enabled" {
		psk = true
	}
	switch {
	case sae && psk:
		return "sae-mixed", ""
	case sae:
		return "sae", ""
	case psk:
		return "psk2+ccmp", "WPA2-only from the controller (no SAE): this interface would lose WPA3"
	default:
		return "psk2+ccmp", fmt.Sprintf("unrecognised key mgmt %v, assumed WPA2-PSK", mgmt)
	}
}

// keyMgmt: hostapd's wpa_key_mgmt for a WPA network, built like a UAP does
// from the controller's list (aaa.N.wpa.key.M.mgmt; "SAE" plus
// wpa3.transition means WPA-PSK too), with FT variants when 802.11r is on.
// "" = leave it to OpenWrt (open networks, or nothing recognised).
func keyMgmt(cfg Cfg, a, enc string, ft bool) string {
	if enc == "none" {
		return ""
	}
	var mgmt []string
	for i := 1; i <= 4; i++ {
		mgmt = append(mgmt, strings.Fields(cfg[fmt.Sprintf("%swpa.key.%d.mgmt", a, i)])...)
	}
	if enc == "sae-mixed" && cfg[a+"wpa3.transition"] == "enabled" {
		mgmt = append(mgmt, "WPA-PSK")
	}
	order := []string{"SAE", "WPA-PSK", "WPA-PSK-SHA256"}
	fts := map[string]string{"SAE": "FT-SAE", "WPA-PSK": "FT-PSK"}
	var out []string
	for _, m := range order {
		if !slices.Contains(mgmt, m) {
			continue
		}
		out = append(out, m)
		if ft && fts[m] != "" {
			out = append(out, fts[m])
		}
	}
	return strings.Join(out, " ")
}

func pmf(status, mode string) string {
	if status != "enabled" {
		return "0"
	}
	switch mode {
	case "2":
		return "2"
	default:
		return "1"
	}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// planSystem: timezone (POSIX TZ string, as OpenWrt's system.timezone) and
// NTP servers. cur must include `uci show system`.
func planSystem(cfg Cfg, cur UCI, r *Result) []Op {
	var ops []Op
	if _, ok := cur["system.@system[0]"]; !ok {
		return nil
	}
	tz := cfg["system.timezone"]
	if tz == "" {
		tz = cfg["locale.timezone"]
	}
	if tz != "" && cur["system.@system[0].timezone"] != tz {
		ops = append(ops, Op{Key: "system.@system[0].timezone", Value: tz, Old: cur["system.@system[0].timezone"]})
		if z := cur["system.@system[0].zonename"]; z != "" {
			// zonename would override timezone where zoneinfo is installed
			ops = append(ops, Op{Del: true, Key: "system.@system[0].zonename", Old: z})
		}
	}
	ops = append(ops, planSyslog(cfg, cur, r)...)
	if cfg["ntpclient.status"] == "enabled" {
		var servers []string
		for _, n := range indices(cfg, "ntpclient") {
			if cfg["ntpclient."+n+".status"] != "disabled" && cfg["ntpclient."+n+".server"] != "" {
				servers = append(servers, cfg["ntpclient."+n+".server"])
			}
		}
		if len(servers) > 0 {
			if _, ok := cur["system.ntp"]; !ok {
				r.Notes = append(r.Notes, "no system.ntp section on the AP: NTP servers not set")
			} else if old := cur["system.ntp.server"]; !listEq(old, servers) {
				ops = append(ops, Op{Key: "system.ntp.server", List: servers, Old: old})
			}
		}
	}
	return ops
}

// quoteEach renders list values the way `uci show` prints them: 'a' 'b'.
func quoteEach(v []string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = "'" + s + "'"
	}
	return out
}

// macACL returns the OpenWrt macfilter policy ("allow", "deny", "disable")
// and MAC list for the VAP devname, from system_cfg's macacl section. An
// empty deny list (the controller's default) is "disable".
func macACL(cfg Cfg, devname string) (string, []string) {
	if cfg["macacl.status"] != "enabled" || devname == "" {
		return "disable", nil
	}
	for _, n := range indices(cfg, "macacl") {
		p := "macacl." + n + "."
		if cfg[p+"devname"] != devname {
			continue
		}
		if cfg[p+"status"] != "enabled" || cfg[p+"acl.status"] != "enabled" {
			return "disable", nil
		}
		var macs []string
		for _, m := range indices(cfg, "macacl."+n+".acl") {
			q := p + "acl." + m + "."
			if cfg[q+"status"] != "disabled" && cfg[q+"mac"] != "" {
				macs = append(macs, strings.ToLower(cfg[q+"mac"]))
			}
		}
		pol := cfg[p+"acl.policy"]
		if pol != "allow" && pol != "deny" {
			return "disable", nil
		}
		if pol == "deny" && len(macs) == 0 {
			return "disable", nil
		}
		return pol, macs
	}
	return "disable", nil
}

// listEq compares a list option as `uci show` prints it (one value: a; more:
// 'a' 'b') with the wanted values.
func listEq(cur string, want []string) bool {
	if len(want) == 1 {
		return cur == want[0] || cur == "'"+want[0]+"'"
	}
	return cur == strings.Join(quoteEach(want), " ")
}
