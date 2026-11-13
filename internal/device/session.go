// SPDX-License-Identifier: GPL-2.0-or-later

// Package device is the device side of the inform protocol: the state a
// UniFi AP keeps (key, cipher, cfgversion, adopted), the payload it sends,
// and what it does with each controller reply. It never applies firmware.
//
// Reply handling: docs/internals.md (open-unifi docs/PROTOCOL-mgmt.md
// §6, unifi-emu inform/session.go).
package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/inform"
)

// State is persisted (mode 600) so a restart keeps the adopted key.
type State struct {
	Key        string           `json:"key"`                   // current authkey (hex); secret
	GCM        bool             `json:"gcm"`                   // encrypt informs with AES-GCM
	Adopted    bool             `json:"adopted"`               // holds a per-device key
	CfgVersion string           `json:"cfgversion"`            // echoed in every inform
	InformURL  string           `json:"inform_url"`            // where to inform
	Version    string           `json:"version"`               // firmware version we report
	MgmtCfg    string           `json:"mgmt_cfg"`              // last mgmt_cfg (may hold authkey); secret
	LED        bool             `json:"led"`                   // mgmt_cfg led_enabled
	Locating   bool             `json:"locating"`              // UniFi "Locate" active
	Blocked    []string         `json:"blocked_sta,omitempty"` // blocked client MACs (controller's blocked_sta)
	Guests     map[string]int64 `json:"guests,omitempty"`      // portal-authorized guest MAC -> expiry (unix s)
	GuestToken string           `json:"guest_token,omitempty"` // portal cookie key (32 hex), sent in informs; secret
	HashID     string           `json:"hash_id,omitempty"`     // device hash (16 hex), sent in informs; names us in encrypted syslog
	SystemCfgN int              `json:"system_cfg_bytes"`
}

func NewState(informURL string) State {
	return State{Key: inform.DefaultKeyHex, CfgVersion: "0", InformURL: informURL, Version: inform.U7PG2Version}
}

func Load(path string) (State, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, false, err
	}
	return s, true, nil
}

func (s State) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Session ties the state to the device description and a boot clock.
type Session struct {
	// GuestMu guards S.Guests (the inform loop and the :8882 portal handler).
	GuestMu  sync.Mutex
	S        State
	Dev      inform.Device
	Boot     time.Time
	StateDir string // where state.json and system_cfg.txt live (mode 700)
	SSHPort  int    // reported as sshd_port: where the console SSHes to adopt
	ledKnown bool
	// Extra, when set, returns fields read from the real AP (radios, VAPs,
	// clients, system); they replace the static U7PG2 defaults.
	Extra func() map[string]any
	// WifiCaps is the UNIFI_WIFI_CAP bitmap we claim (0 = not sent). Claim
	// only what the AP really does: the controller trusts it.
	WifiCaps int
	// Optional further capability words (0 = not sent / default).
	WifiCaps2, FWCaps, FW2Caps int
	// HWCaps: hardware bits (2 = LED bar colour, 16384 = LED brightness):
	// the controller offers LED colour/brightness only when they are set.
	HWCaps int
}

func (x *Session) Cipher() inform.Cipher {
	if x.S.GCM {
		return inform.GCM
	}
	return inform.CBC
}

// Payload builds the next inform body.
func (x *Session) Payload(now time.Time) ([]byte, error) {
	d := x.Dev
	d.Uptime = int64(now.Sub(x.Boot).Seconds())
	d.Time = now.Unix()
	d.InformURL = x.S.InformURL
	m, err := inform.U7PG2Fields(d)
	if err != nil {
		return nil, err
	}
	if x.Extra != nil {
		for k, v := range x.Extra() {
			m[k] = v
		}
	}
	m["version"] = x.S.Version
	m["cfgversion"] = x.S.CfgVersion
	m["x_authkey"] = x.S.Key
	m["locating"] = x.S.Locating
	if x.S.HashID != "" {
		m["hash_id"] = x.S.HashID
	}
	if x.S.GuestToken != "" {
		m["guest_token"] = x.S.GuestToken
		m["guest_kicks"] = 0
	}
	// The fastest inform interval we support. 1 = "supports live updates"
	// (com.ubnt.data device: supportsLiveUpdates): the controller then asks
	// for 1 s informs while the device is watched in the panel, and the
	// panel draws its clients' live signal graph (docs/internals.md).
	m["inform_min_interval"] = 1
	if x.WifiCaps != 0 {
		m["wifi_caps"] = uint32(x.WifiCaps) // 32-bit word: on 32-bit ARM an int holding it is negative
	}
	if x.WifiCaps2 != 0 {
		m["wifi_caps2"] = uint32(x.WifiCaps2) // 32-bit word: on 32-bit ARM an int holding it is negative
	}
	if x.FWCaps != 0 {
		m["fw_caps"] = uint32(x.FWCaps) // 32-bit word: on 32-bit ARM an int holding it is negative
	}
	if x.HWCaps != 0 {
		m["hw_caps"] = uint32(x.HWCaps)
	}
	if x.FW2Caps != 0 {
		m["fw2_caps"] = uint32(x.FW2Caps) // 32-bit word: on 32-bit ARM an int holding it is negative
	}
	if x.SSHPort > 0 {
		m["sshd_port"] = x.SSHPort
	}
	if x.S.Adopted {
		m["default"] = false
		m["_default_key"] = false
		m["state"] = 4 // device-side "managed"; the controller does not read it
		m["x_aes_gcm"] = x.S.GCM
		m["bootrom_version"] = "unknown"
		m["sys_stats"] = map[string]any{
			"loadavg_1": "0.05", "loadavg_5": "0.04", "loadavg_15": "0.01",
			"mem_total": 129130496, "mem_used": 64565248, "mem_buffer": 4194304,
		}
		m["radio_table_stats"] = radioStats(d.Radios)
	}
	return json.Marshal(m)
}

func radioStats(rs []inform.Radio) []map[string]any {
	if rs == nil {
		rs = inform.DefaultU7PG2Radios()
	}
	var out []map[string]any
	for _, r := range rs {
		out = append(out, map[string]any{"name": r.Name, "radio": r.Radio, "channel": r.Channel,
			"tx_power": r.MaxTxpower, "num_sta": 0, "cu_total": 0, "cu_self_rx": 0, "cu_self_tx": 0})
	}
	return out
}

// Reply is a decoded controller reply.
type Reply struct {
	Type       string  `json:"_type"`
	Interval   int     `json:"interval"`
	Immediate  int     `json:"immediate"`
	Cmd        string  `json:"cmd"`
	Key        string  `json:"key"`
	URI        string  `json:"uri"`
	Version    string  `json:"version"`
	URL        string  `json:"url"`
	CfgVersion string  `json:"cfgversion"`
	MgmtCfg    string  `json:"mgmt_cfg"`
	SystemCfg  string  `json:"system_cfg"`
	BlockedSta *string `json:"blocked_sta"` // nil = not sent; "" = no blocked clients
	MAC        string  `json:"mac"`         // cmd kick-sta / block-sta / unblock-sta / (un)authorize-guest
	Minutes    int     `json:"minutes"`     // authorize-guest: duration, if sent
	End        int64   `json:"end"`         // authorize-guest: end time (unix s), if sent
}

// Outcome tells the loop what happened and what to do next.
type Outcome struct {
	Events    []string      // human log lines (no secrets)
	Interval  time.Duration // 0 = keep the current interval
	Immediate bool          // inform again right away
	Changed   bool          // state changed: save it
	Reset     bool          // controller sent setdefault (device was removed)
	Actions   []string      // for a real AP: "reboot", "led:on", "led:off", "locate:on", "locate:off"
}

// Handle applies a decoded reply to the session.
func (x *Session) Handle(js []byte, now time.Time) (Outcome, error) {
	var r Reply
	if err := json.Unmarshal(js, &r); err != nil {
		return Outcome{}, fmt.Errorf("reply is not JSON: %w", err)
	}
	var o Outcome
	if r.Interval > 0 {
		o.Interval = time.Duration(r.Interval) * time.Second
	}
	switch r.Type {
	case "noop":
		o.Immediate = r.Immediate != 0
		o.Events = append(o.Events, fmt.Sprintf("noop interval=%d immediate=%d", r.Interval, r.Immediate))
	case "setparam":
		x.setparam(r, &o)
	case "upgrade":
		x.upgrade(r.Version, r.URL, now, &o)
	case "reboot":
		x.Boot = now
		o.Immediate = true
		o.Actions = append(o.Actions, "reboot")
		o.Events = append(o.Events, "reboot requested")
	case "setdefault":
		x.setdefault(&o)
	case "cmd":
		x.cmd(r, now, &o)
	default:
		o.Events = append(o.Events, fmt.Sprintf("reply type %q: ignored", r.Type))
	}
	if strings.Contains(r.Cmd, "guest") {
		o.Events = append(o.Events, "  raw reply: "+MaskHex(string(js)))
	}
	for _, e := range o.Events {
		if strings.Contains(e, "ignored") {
			o.Events = append(o.Events, "  raw reply: "+MaskHex(string(js)))
			break
		}
	}
	return o, nil
}

func (x *Session) setparam(r Reply, o *Outcome) {
	var cv, authkey, gcm string
	var keys []string
	for _, line := range strings.Split(r.MgmtCfg, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		keys = append(keys, k)
		switch k {
		case "cfgversion":
			cv = v
		case "authkey":
			authkey = v
		case "use_aes_gcm":
			gcm = v
		case "led_enabled":
			on := v == "true"
			if on != x.S.LED || !x.ledKnown {
				x.S.LED, x.ledKnown = on, true
				o.Changed = true
				o.Actions = append(o.Actions, map[bool]string{true: "led:on", false: "led:off"}[on])
			}
		}
	}
	if r.CfgVersion != "" {
		cv = r.CfgVersion // full provisioning carries it top-level too
	}
	ev := fmt.Sprintf("setparam: mgmt_cfg keys=%v", keys)
	if r.MgmtCfg != "" {
		x.S.MgmtCfg = r.MgmtCfg
		o.Changed = true
	}
	if cv != "" && cv != x.S.CfgVersion {
		ev += fmt.Sprintf(" cfgversion %s->%s", x.S.CfgVersion, cv)
		x.S.CfgVersion = cv
		o.Changed = true
	}
	// Take a new key only while still on the factory key (unifi-emu: the
	// classic stuck-adopt bug otherwise).
	if authkey != "" && authkey != inform.DefaultKeyHex {
		if x.S.Key == inform.DefaultKeyHex {
			x.S.Key = authkey
			x.S.Adopted = true
			o.Changed = true
			ev += " ADOPTED: new authkey stored"
		} else if authkey != x.S.Key {
			ev += " (ignored a different authkey: already hold a non-default key)"
		}
	}
	if gcm == "true" && !x.S.GCM {
		x.S.GCM = true
		o.Changed = true
		ev += " GCM on"
	}
	if r.SystemCfg != "" {
		x.S.SystemCfgN = len(r.SystemCfg)
		o.Changed = true
		path := filepath.Join(x.StateDir, "system_cfg.txt")
		if err := os.WriteFile(path, []byte(r.SystemCfg), 0o600); err != nil {
			ev += " (saving system_cfg failed: " + err.Error() + ")"
		}
		// keep every version, to see what the controller changed
		hist := filepath.Join(x.StateDir, "system_cfg.history")
		os.MkdirAll(hist, 0o700)
		os.WriteFile(filepath.Join(hist, time.Now().Format("20060102-150405")+"-"+x.S.CfgVersion+".txt"), []byte(r.SystemCfg), 0o600)
		pruneHistory(hist, HistoryKeep)
		ev += fmt.Sprintf(" system_cfg %d bytes saved (not applied), sections=%v", len(r.SystemCfg), sections(r.SystemCfg))
	}
	if r.BlockedSta != nil {
		ev += fmt.Sprintf(" blocked_sta=%d", len(strings.Fields(*r.BlockedSta)))
		x.setBlocked(ParseMACs(*r.BlockedSta), o)
	}
	o.Events = append(o.Events, ev)
	o.Immediate = true // a real device informs right after applying a setparam
}

// sections lists the top-level keys of system_cfg (e.g. "aaa", "radio",
// "wireless"), never their values: system_cfg carries the Wi-Fi passphrase.
func sections(cfg string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(cfg, "\n") {
		k, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		top, _, _ := strings.Cut(k, ".")
		if !seen[top] {
			seen[top] = true
			out = append(out, top)
		}
	}
	return out
}

func (x *Session) upgrade(version, url string, now time.Time, o *Outcome) {
	// Never download or apply anything. Report the target version and fake
	// a reboot so the controller does not loop on the upgrade.
	host := url
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	o.Events = append(o.Events, fmt.Sprintf("UPGRADE requested to %q (from host %s): NOT applied, nothing downloaded; reporting the version and faking a reboot", version, host))
	if version != "" && version != x.S.Version {
		x.S.Version = version
		o.Changed = true
	}
	x.Boot = now
	o.Immediate = true
}

func (x *Session) setdefault(o *Outcome) {
	x.S.Key = inform.DefaultKeyHex
	x.S.GCM = false
	x.S.Adopted = false
	x.S.CfgVersion = "0"
	x.S.MgmtCfg = ""
	o.Changed = true
	o.Immediate = true
	o.Reset = true
	o.Events = append(o.Events, "setdefault: back to the factory key (the AP itself is not reset)")
}

func (x *Session) cmd(r Reply, now time.Time, o *Outcome) {
	switch r.Cmd {
	case "set-adopt", "adopt":
		if r.Key != "" {
			x.S.Key = r.Key
			x.S.Adopted = true
		}
		if r.URI != "" {
			x.S.InformURL = r.URI
		}
		o.Changed = true
		o.Immediate = true
		o.Events = append(o.Events, fmt.Sprintf("cmd %s: key stored, uri=%q", r.Cmd, r.URI))
	case "reboot", "restart":
		x.Boot = now
		o.Immediate = true
		o.Actions = append(o.Actions, "reboot")
		o.Events = append(o.Events, "cmd "+r.Cmd+": reboot requested")
	case "locate", "set-locate", "locate-on":
		x.S.Locating = true
		o.Changed, o.Immediate = true, true
		o.Actions = append(o.Actions, "locate:on")
		o.Events = append(o.Events, "cmd "+r.Cmd+": locate on")
	case "unset-locate", "locate-off", "stop-locate":
		x.S.Locating = false
		o.Changed, o.Immediate = true, true
		o.Actions = append(o.Actions, "locate:off")
		o.Events = append(o.Events, "cmd "+r.Cmd+": locate off")
	case "setdefault":
		x.setdefault(o)
	case "upgrade":
		x.upgrade(r.Version, r.URL, now, o)
	case "kick-sta", "kick", "disassoc-sta":
		m := ParseMACs(r.MAC)
		if len(m) != 1 {
			o.Events = append(o.Events, fmt.Sprintf("cmd %s: bad mac %q: ignored", r.Cmd, r.MAC))
			break
		}
		o.Immediate = true
		o.Actions = append(o.Actions, "kick:"+m[0])
		o.Events = append(o.Events, "cmd "+r.Cmd+": kick "+m[0])
	case "authorize-guest", "unauthorize-guest":
		m := ParseMACs(r.MAC)
		if len(m) != 1 {
			o.Events = append(o.Events, fmt.Sprintf("cmd %s: bad mac %q: ignored", r.Cmd, r.MAC))
			break
		}
		x.GuestMu.Lock()
		defer x.GuestMu.Unlock()
		if x.S.Guests == nil {
			x.S.Guests = map[string]int64{}
		}
		o.Changed, o.Immediate = true, true
		if r.Cmd == "unauthorize-guest" {
			delete(x.S.Guests, m[0])
			o.Actions = append(o.Actions, "guest-unauth:"+m[0])
			o.Events = append(o.Events, "cmd unauthorize-guest "+m[0])
			break
		}
		end := now.Add(8 * time.Hour).Unix() // the controller's default (expire 480 min)
		switch {
		case r.End > now.Unix():
			end = r.End
		case r.Minutes > 0:
			end = now.Add(time.Duration(r.Minutes) * time.Minute).Unix()
		}
		x.S.Guests[m[0]] = end
		o.Actions = append(o.Actions, "guest-auth:"+m[0])
		o.Events = append(o.Events, fmt.Sprintf("cmd authorize-guest %s until %s", m[0], time.Unix(end, 0).Format("15:04")))
	case "block-sta", "unblock-sta":
		m := ParseMACs(r.MAC)
		if len(m) != 1 {
			o.Events = append(o.Events, fmt.Sprintf("cmd %s: bad mac %q: ignored", r.Cmd, r.MAC))
			break
		}
		set := map[string]bool{}
		for _, b := range x.S.Blocked {
			set[b] = true
		}
		set[m[0]] = r.Cmd == "block-sta"
		var list []string
		for k, v := range set {
			if v {
				list = append(list, k)
			}
		}
		o.Events = append(o.Events, "cmd "+r.Cmd+" "+m[0])
		x.setBlocked(list, o)
	case "scan", "spectrum-scan":
		// neighbour scan / RF scan: the daemon scans the radios and reports
		// scan_table / spectrum_table (docs/internals.md)
		o.Immediate = true
		o.Actions = append(o.Actions, r.Cmd)
		o.Events = append(o.Events, "cmd "+r.Cmd)
	default:
		o.Events = append(o.Events, fmt.Sprintf("cmd %q: ignored", r.Cmd))
	}
}

// MaskHex hides 32-hex keys in a string (for logging raw replies).
func MaskHex(s string) string { return hex32.ReplaceAllString(s, "<masked>") }

// RestoreFromMgmtCfg re-reads settings carried by the last saved mgmt_cfg
// (LED) after a restart.
func (x *Session) RestoreFromMgmtCfg() {
	for _, line := range strings.Split(x.S.MgmtCfg, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "led_enabled="); ok {
			x.S.LED, x.ledKnown = v == "true", true
		}
	}
}

var macRe = regexp.MustCompile(`(?i)^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// ParseMACs reads a whitespace-separated MAC list (the blocked_sta wire
// format is newline-joined), lower-cased, sorted, without duplicates or
// malformed entries.
func ParseMACs(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.ToLower(f)
		if macRe.MatchString(f) && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// setBlocked records the blocked-client list; a change asks the AP to
// enforce it ("block" action).
func (x *Session) setBlocked(list []string, o *Outcome) {
	sort.Strings(list)
	if strings.Join(list, " ") == strings.Join(x.S.Blocked, " ") {
		return
	}
	now := map[string]bool{}
	for _, m := range list {
		now[m] = true
	}
	for _, m := range x.S.Blocked {
		if !now[m] {
			o.Actions = append(o.Actions, "unblock:"+m)
		}
	}
	x.S.Blocked = list
	o.Changed = true
	o.Actions = append(o.Actions, "block")
	o.Events = append(o.Events, fmt.Sprintf("blocked clients now %d", len(list)))
}

// HistoryKeep is how many system_cfg versions are kept (on an AP the state
// directory is on flash: it must not grow).
var HistoryKeep = 20

// pruneHistory keeps the newest n files in dir (names sort by time).
func pruneHistory(dir string, n int) {
	es, err := os.ReadDir(dir)
	if err != nil || len(es) <= n {
		return
	}
	var names []string
	for _, e := range es {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names[:max(0, len(names)-n)] {
		os.Remove(filepath.Join(dir, name))
	}
}
