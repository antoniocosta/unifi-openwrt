// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"unifi-openwrt/internal/translate"
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

// Apply applies ops to the AP's wireless config with an automatic rollback:
// the AP restores its previous config after `rollback` unless Confirm is
// called. It returns an id for Confirm/RollbackNow.
func (cl *Client) Apply(ops []translate.Op, rollback time.Duration) (string, error) {
	id := time.Now().Format("20060102-150405")
	batch, script, err := applyScript(ops, rollback, id)
	if err != nil {
		return "", err
	}
	// stdin = the batch only; the script reads it via `uci batch` (stdin).
	out, err := cl.RunStdin("sh -c "+shQuote(script), []byte(batch))
	if err != nil {
		return id, fmt.Errorf("apply: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), "applied") {
		return id, fmt.Errorf("apply: unexpected output %q", out)
	}
	return id, nil
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
			reload = append(reload, "wifi reload")
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
setsid sh /tmp/unifi-rollback-%[1]s.sh </dev/null >/dev/null 2>&1 &
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

// WifiHealthy checks that every enabled radio with interfaces is up. A
// radio with no (enabled) interfaces is fine: a disabled device (Disable in
// the UniFi) gets no WLANs at all (docs/internals.md).
func (cl *Client) WifiHealthy() (bool, string) {
	out, err := cl.Run("ubus call network.wireless status")
	if err != nil {
		return false, err.Error()
	}
	return wifiStatusOK(out)
}

func wifiStatusOK(js []byte) (bool, string) {
	// Parse {"radio0":{"up":true,"disabled":false,"interfaces":[...]},...}
	type radio struct {
		Up         bool  `json:"up"`
		Disabled   bool  `json:"disabled"`
		Interfaces []any `json:"interfaces"`
	}
	var m map[string]radio
	if err := json.Unmarshal(js, &m); err != nil {
		return false, "wireless status: " + err.Error()
	}
	var bad []string
	for name, r := range m {
		if r.Disabled {
			continue
		}
		if len(r.Interfaces) > 0 && !r.Up {
			bad = append(bad, name)
		}
	}
	if len(bad) > 0 {
		return false, fmt.Sprintf("radios not up: %v", bad)
	}
	return true, fmt.Sprintf("%d radios ok", len(m))
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// featureScript prints the optional hostapd features this AP has. hostapd
// has -v probes for 11r (and 11ac, 11ax, sae, ...), but not for 802.11k/v:
// those are detected by their config keys in the binary (bss_transition is
// only compiled in with CONFIG_WNM_AP).
const featureScript = `b=$(readlink -f "$(command -v hostapd)"); [ -n "$b" ] || exit 0
hostapd -v11r >/dev/null 2>&1 && echo 11r
grep -q -a rrm_neighbor_report "$b" && echo 11k
grep -q -a bss_transition "$b" && echo 11v
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
		f.Phys = snap.RadioNames()
	}
	return f
}
