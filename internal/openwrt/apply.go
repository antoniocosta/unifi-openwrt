// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// RunStdin runs a command with the given stdin (used so secrets travel in
// the session's input, never in a command line).
func (cl *Client) RunStdin(cmd string, stdin []byte) ([]byte, error) {
	if cl.t.Addr == "local" {
		if stdin == nil {
			stdin = []byte{}
		}
		return localRun(cmd, stdin)
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	c, err := cl.conn()
	if err != nil {
		return nil, err
	}
	s, err := c.NewSession()
	if err != nil {
		c.Close()
		cl.c = nil
		return nil, err
	}
	defer s.Close()
	var out, errb bytes.Buffer
	s.Stdout, s.Stderr = &out, &errb
	s.Stdin = bytes.NewReader(stdin)
	if err := s.Run(cmd); err != nil {
		return out.Bytes(), fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// UCIBatch renders ops as `uci batch` input. Values are quoted for uci's
// parser; a value with a newline is refused.
func UCIBatch(ops []translate.Op) (string, error) {
	var b strings.Builder
	for _, op := range ops {
		if op.List != nil {
			fmt.Fprintf(&b, "delete %s\n", op.Key)
			for _, v := range op.List {
				if strings.ContainsAny(v, "\n\r") {
					return "", fmt.Errorf("refusing value with a newline for %s", op.Key)
				}
				fmt.Fprintf(&b, "add_list %s=%s\n", op.Key, uciQuote(v))
			}
			continue
		}
		if op.Del {
			fmt.Fprintf(&b, "delete %s\n", op.Key)
			continue
		}
		if strings.ContainsAny(op.Value, "\n\r") {
			return "", fmt.Errorf("refusing value with a newline for %s", op.Key)
		}
		fmt.Fprintf(&b, "set %s=%s\n", op.Key, uciQuote(op.Value))
	}
	return b.String(), nil
}

func uciQuote(v string) string {
	if !strings.Contains(v, "'") {
		return "'" + v + "'"
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(v) + `"`
}

// LiveLists is what Apply needs to change MAC filters without a Wi-Fi
// reload (LiveACL).
type LiveLists struct {
	ACLs []translate.ACL // the wanted filter of every planned WLAN section
	Keep []string        // blocked clients: never taken off a deny list
}

// Apply applies ops to the AP's wireless config with an automatic rollback:
// the AP restores its previous config after `rollback` unless Confirm is
// called. It returns an id for Confirm/RollbackNow, and how the Wi-Fi took
// the change: "reload" (the plan's normal reloads, as before), "live" (MAC
// filters changed in hostapd, no reload; see LiveACL), or "reload:
// <reason>" (a live change was not possible, so it reloaded).
func (cl *Client) Apply(ops []translate.Op, ll LiveLists, rollback time.Duration) (string, string, error) {
	id := time.Now().Format("20060102-150405")
	batch, script, err := applyScript(ops, rollback, id)
	if err != nil {
		return "", "", err
	}
	how := "reload"
	if changed, ok := LiveACL(ops, ll.ACLs); ok {
		if b, s, err := liveApplyScript(ops, changed, ll.Keep, rollback, id); err != nil {
			how = "reload: " + err.Error()
		} else {
			batch, script, how = b, s, "live"
		}
	}
	// stdin = the batch only; the script reads it via `uci batch` (stdin).
	out, err := cl.RunStdin("sh -c "+shQuote(script), []byte(batch))
	if err != nil {
		return id, how, fmt.Errorf("apply: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "applied") {
		return id, how, fmt.Errorf("apply: unexpected output %q", out)
	}
	if how == "live" {
		how = liveOutcome(string(out))
	}
	return id, how, nil
}

// wifiReload is an apply's Wi-Fi reload: the scan interface may sit on a
// radio that gets an AP now; usually it is not there (set -e: iw's ENODEV
// must not stop us).
const wifiReload = "iw dev " + ScanIface + " del 2>/dev/null || true; wifi reload"

// liveApplyScript is applyScript for a plan of MAC filter changes only
// (LiveACL): the same backup, rollback timer and commit, with liveBlock in
// place of the Wi-Fi reload. The rollback still restores and reloads.
func liveApplyScript(ops []translate.Op, changed []translate.ACL, keep []string, rollback time.Duration, id string) (string, string, error) {
	batch, script, err := applyScript(ops, rollback, id)
	if err != nil {
		return "", "", err
	}
	block, err := liveBlock(changed, keep, "/var/run", wifiReload)
	if err != nil {
		return "", "", err
	}
	if strings.Count(script, wifiReload+"\n") != 1 {
		return "", "", fmt.Errorf("no single Wi-Fi reload in the apply script")
	}
	return batch, strings.Replace(script, wifiReload+"\n", block, 1), nil
}

// applyScript builds the uci batch and the shell script for Apply.
func applyScript(ops []translate.Op, rollback time.Duration, id string) (string, string, error) {
	batch, err := UCIBatch(ops)
	if err != nil {
		return "", "", err
	}
	pkgs := map[string]bool{}
	for _, op := range ops {
		p, _, _ := strings.Cut(op.Key, ".")
		pkgs[p] = true
	}
	var backup, restore, commit, reload []string
	for _, p := range []string{"wireless", "system", "network"} {
		if !pkgs[p] {
			continue
		}
		backup = append(backup, fmt.Sprintf("cp -p /etc/config/%[1]s /etc/config/%[1]s.pre-unifi-%[2]s", p, id))
		restore = append(restore, fmt.Sprintf("cp -p /etc/config/%[1]s.pre-unifi-%[2]s /etc/config/%[1]s", p, id))
		commit = append(commit, "uci commit "+p)
		switch p {
		case "wireless":
			reload = append(reload, wifiReload)
		case "system":
			reload = append(reload, "/etc/init.d/system reload; /etc/init.d/sysntpd restart")
		case "network":
			// detached: the reload may drop this SSH session
			reload = append(reload, "(sleep 1; /etc/init.d/network reload) </dev/null >/dev/null 2>&1 &")
		}
		delete(pkgs, p)
	}
	if len(pkgs) > 0 {
		return "", "", fmt.Errorf("refusing ops on packages %v", pkgs)
	}
	rbReload := strings.ReplaceAll(strings.Join(reload, "\n"), "wifi reload", "wifi")
	script := fmt.Sprintf(`set -e
%[3]s
cat > /tmp/unifi-rollback-%[1]s.sh <<'RB'
#!/bin/sh
sleep %[2]d
if [ -f /tmp/unifi-ok-%[1]s ]; then
%[8]s
exit 0
fi
logger -t unifi-openwrt "apply %[1]s not confirmed: rolling back"
%[4]s
%[5]s
%[8]s
RB
chmod +x /tmp/unifi-rollback-%[1]s.sh
# the timer must outlive this session; not every image has setsid (a
# BusyBox without the applet: the timer would silently never run)
if command -v setsid >/dev/null 2>&1; then
	setsid sh /tmp/unifi-rollback-%[1]s.sh </dev/null >/dev/null 2>&1 &
else
	(trap "" HUP; exec sh /tmp/unifi-rollback-%[1]s.sh) </dev/null >/dev/null 2>&1 &
fi
uci batch
%[6]s
%[7]s
echo applied
`, id, int(rollback.Seconds()), strings.Join(backup, "\n"), strings.Join(restore, "\n"), rbReload,
		strings.Join(commit, "\n"), strings.Join(reload, "\n"), cleanupScript(id))
	return batch, script, nil
}

// Confirm cancels the rollback of apply id.
func (cl *Client) Confirm(id string) error {
	// only the marker: the rollback timer, still sleeping, checks it and then
	// cleans up after itself (deleting the marker here would roll back)
	_, err := cl.Run(fmt.Sprintf("touch /tmp/unifi-ok-%s", id))
	return err
}

// cleanupScript removes apply id's backups and rollback files and keeps at
// most 3 backups per package (the AP's /etc is flash). Run only by the
// rollback timer itself (after it decided) or after RollbackNow.
func cleanupScript(id string) string {
	return fmt.Sprintf(`rm -f /etc/config/*.pre-unifi-%[1]s /tmp/unifi-rollback-%[1]s.sh /tmp/unifi-ok-%[1]s
for p in wireless system network; do ls -1 /etc/config/$p.pre-unifi-* 2>/dev/null | sort -r | tail -n +4 | xargs -r rm -f; done`, id)
}

// RollbackNow restores the config saved by apply id right away.
func (cl *Client) RollbackNow(id string) error {
	_, err := cl.Run(fmt.Sprintf(`touch /tmp/unifi-ok-%[1]s
for p in wireless system network; do [ -f /etc/config/$p.pre-unifi-%[1]s ] && cp -p /etc/config/$p.pre-unifi-%[1]s /etc/config/$p; done
[ -f /etc/config/system.pre-unifi-%[1]s ] && { /etc/init.d/system reload; /etc/init.d/sysntpd restart; }
[ -f /etc/config/network.pre-unifi-%[1]s ] && { (sleep 1; /etc/init.d/network reload) </dev/null >/dev/null 2>&1 & }
wifi`, id))
	return err
}

// wifiProbe prints netifd's view of the radios, hostapd's state of every BSS
// ("hostapd.<ifname> <state>") and the kernel's interfaces.
const wifiProbe = `ubus call network.wireless status || exit 1
echo "=== hostapd"
for o in $(ubus list "hostapd.*" 2>/dev/null); do
  echo "$o $(ubus call "$o" get_status 2>/dev/null | jsonfilter -e "@.status" 2>/dev/null)"
done
echo "=== iw"
iw dev 2>/dev/null
true`

// WifiHealthy checks that every enabled radio with interfaces is up and that
// each of its AP interfaces is on the air. A radio with no (enabled)
// interfaces is fine: a disabled device (Disable in the UniFi) gets no WLANs
// at all (docs/internals.md).
func (cl *Client) WifiHealthy() (bool, string) {
	ok, _, why := cl.WifiHealth()
	return ok, why
}

// WifiHealth is WifiHealthy plus starting: nothing is broken, but a BSS is
// still on its way up (hostapd is choosing a channel or scanning for
// neighbours), so the caller may look again.
func (cl *Client) WifiHealth() (ok, starting bool, why string) {
	out, err := cl.Run(wifiProbe)
	if err != nil {
		return false, false, err.Error()
	}
	return wifiHealth(out)
}

func wifiStatusOK(js []byte) (bool, string) {
	ok, _, why := wifiHealth(js)
	return ok, why
}

// wifiHealth reads wifiProbe's output. netifd reports a radio as up as soon
// as its setup script has run, also when hostapd then failed to start the
// BSSes (seen with a failed automatic channel selection: WLANs off, radio
// "up"). So every AP interface is checked too:
//   - hostapd says ENABLED, or DFS (the radar check before the first beacon,
//     up to 10 minutes on some channels): fine.
//   - hostapd says ACS, HT_SCAN or COUNTRY_UPDATE, or netifd has no name for
//     the interface yet: starting.
//   - hostapd says anything else (DISABLED, UNINITIALIZED): not on the air.
//   - hostapd has no answer for it (no ubus object under that name): the
//     kernel's interface must be an AP on a channel.
//
// Output without the hostapd and iw parts (only the status JSON) checks the
// radios alone.
func wifiHealth(out []byte) (ok, starting bool, why string) {
	js, rest, probed := strings.Cut(string(out), "\n=== hostapd\n")
	hp, iw, _ := strings.Cut(rest, "=== iw\n")
	type radio struct {
		Up         bool `json:"up"`
		Disabled   bool `json:"disabled"`
		Interfaces []struct {
			Ifname string `json:"ifname"`
			Config struct {
				Mode string `json:"mode"`
			} `json:"config"`
		} `json:"interfaces"`
	}
	var m map[string]radio
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		return false, false, "wireless status: " + err.Error()
	}
	state := map[string]string{} // ifname -> hostapd state
	for _, l := range strings.Split(hp, "\n") {
		if name, st, found := strings.Cut(strings.TrimPrefix(l, "hostapd."), " "); found && strings.HasPrefix(l, "hostapd.") && st != "" {
			state[name] = st
		}
	}
	onAir := map[string]bool{} // kernel interfaces of type AP with a channel
	var cur string
	var isAP bool
	for _, l := range strings.Split(iw, "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 2 && f[0] == "Interface":
			cur, isAP = f[1], false
		case len(f) == 2 && f[0] == "type":
			isAP = f[1] == "AP"
		case len(f) > 1 && f[0] == "channel" && isAP:
			onAir[cur] = true
		}
	}
	var down, off, wait []string
	for name, r := range m {
		if r.Disabled || len(r.Interfaces) == 0 {
			continue
		}
		if !r.Up {
			down = append(down, name)
			continue
		}
		if !probed {
			continue
		}
		for _, i := range r.Interfaces {
			if i.Config.Mode != "ap" {
				continue
			}
			switch st := state[i.Ifname]; {
			case i.Ifname == "":
				wait = append(wait, name+" (an interface without a name yet)")
			case st == "ENABLED" || st == "DFS":
			case st == "ACS" || st == "HT_SCAN" || st == "COUNTRY_UPDATE":
				wait = append(wait, i.Ifname+" ("+st+")")
			case st != "":
				off = append(off, i.Ifname+" ("+st+")")
			case !onAir[i.Ifname]:
				off = append(off, i.Ifname+" (no channel)")
			}
		}
	}
	sort.Strings(down)
	sort.Strings(off)
	sort.Strings(wait)
	switch {
	case len(down) > 0:
		return false, false, fmt.Sprintf("radios not up: %v", down)
	case len(off) > 0:
		return false, false, fmt.Sprintf("WLANs not on the air: %v", off)
	case len(wait) > 0:
		return false, true, fmt.Sprintf("WLANs still starting: %v", wait)
	}
	return true, false, fmt.Sprintf("%d radios ok", len(m))
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// featureScript prints the optional hostapd features this AP has. hostapd
// has -v probes for 11r (and 11ac, 11ax, sae, ...), but not for 802.11k/v:
// those are detected by their config keys in the binary (bss_transition is
// only compiled in with CONFIG_WNM_AP). bss_transition must be a whole
// string: a wpad without WNM still holds "bss_transition_request" and
// friends, and hostapd then rejects the bss_transition line.
const featureScript = `b=$(readlink -f "$(command -v hostapd)"); [ -n "$b" ] || exit 0
hostapd -v11r >/dev/null 2>&1 && echo 11r
grep -q -a rrm_neighbor_report "$b" && echo 11k
tr '\0' '\n' < "$b" | grep -qx bss_transition && echo 11v
true`

// Features reports what the AP's hostapd supports, plus its radios.
func (cl *Client) Features() translate.Features {
	out, _ := cl.Run(featureScript)
	has := map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		has[l] = true
	}
	f := translate.Features{K: has["11k"], V: has["11v"], R: has["11r"]}
	if snap, err := cl.Collect(); err == nil {
		f.Phys, f.Sections, f.TxPower = snap.RadioNames(), snap.RadioSections(), snap.RadioTxPower()
	}
	return f
}
