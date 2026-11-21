// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"errors"
	"fmt"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// MAC filter changes without a Wi-Fi reload.
//
// On a `wifi reload` a changed maclist or macfilter restarts each affected
// BSS, and every client of it drops. hostapd changes both lists at runtime
// (hostapd_cli deny_acl|accept_acl ADD_MAC|DEL_MAC|SHOW) and disconnects a
// client that a change denies. So a plan that changes nothing but MAC
// filters, and switches no WLAN between deny and allow (hostapd's
// macaddr_acl), is applied there. The UCI change is committed all the same,
// so the next start of hostapd reads the same lists.

// LiveACL reports whether ops change only the MAC filters of WLAN sections
// in acls, without switching one between deny and allow ("" and "deny" are
// both hostapd's "accept unless denied"). It returns the wanted filter of
// each section with a change, in the order of acls.
func LiveACL(ops []translate.Op, acls []translate.ACL) ([]translate.ACL, bool) {
	if len(ops) == 0 {
		return nil, false
	}
	known := map[string]bool{}
	for _, a := range acls {
		known[a.Section] = true
	}
	touched := map[string]bool{}
	oldPolicy := map[string]string{} // sections whose macfilter changes
	for _, op := range ops {
		rest, ok := strings.CutPrefix(op.Key, "wireless.")
		if !ok {
			return nil, false
		}
		sec, opt, ok := strings.Cut(rest, ".")
		if !ok || !known[sec] || opt != "maclist" && opt != "macfilter" {
			return nil, false
		}
		touched[sec] = true
		if opt == "macfilter" {
			oldPolicy[sec] = op.Old
		}
	}
	var out []translate.ACL
	for _, a := range acls {
		if !touched[a.Section] {
			continue
		}
		from, changed := oldPolicy[a.Section]
		if !changed {
			from = a.Policy
		}
		if !sameACLMode(from, a.Policy) {
			return nil, false
		}
		out = append(out, a)
	}
	return out, true
}

// sameACLMode reports whether hostapd's macaddr_acl stays the same: "" and
// "deny" accept unless denied, "allow" denies unless accepted.
func sameACLMode(from, to string) bool {
	denyish := func(p string) bool { return p == "" || p == "deny" }
	return denyish(from) && denyish(to) || from == "allow" && to == "allow"
}

// liveFuncs: uow_live <section> <deny_acl|accept_acl> <write|delete>
// [mac...] brings hostapd's list on every running interface of the section
// to the wanted MACs, never taking $UOW_KEEP off a deny list, rewrites the
// maclist file OpenWrt would write, and checks the result. On a problem it
// sets uow_err and returns 1. It runs in an if condition, so set -e does
// not apply inside it: it reports every failure itself. It avoids special
// builtins with redirections (": > file"), whose failed redirection would end
// the whole shell instead of returning.
const liveFuncs = `uow_err=
uow_live() {
	uow_sec=$1 uow_acl=$2 uow_file=$3
	shift 3
	uow_st=$(ubus call network.wireless status 2>/dev/null) || { uow_err="ubus network.wireless status failed"; return 1; }
	uow_ifs=$(printf '%s' "$uow_st" | jsonfilter -e "@.*.interfaces[@.section='$uow_sec'].ifname" 2>/dev/null) || uow_ifs=
	uow_ifs=$(printf '%s\n' "$uow_ifs" | tr '\n' ' ')
	case "$uow_ifs" in
	*[!A-Za-z0-9_.\ -]*) uow_err="bad interface name for $uow_sec"; return 1 ;;
	esac
	for uow_if in $uow_ifs; do
		if [ ${#uow_if} -gt 15 ]; then uow_err="bad interface name for $uow_sec"; return 1; fi
		if [ ! -S "$UOW_RUN/hostapd/$uow_if" ]; then uow_err="no hostapd control socket for $uow_if"; return 1; fi
		uow_raw=$(hostapd_cli -p "$UOW_RUN/hostapd" -i "$uow_if" $uow_acl SHOW 2>/dev/null) || { uow_err="$uow_acl SHOW failed on $uow_if"; return 1; }
		if [ ${#uow_raw} -ge 4000 ]; then uow_err="$uow_acl list too long for hostapd_cli SHOW on $uow_if"; return 1; fi
		uow_have=" $(printf '%s\n' "$uow_raw" | cut -d' ' -f1 | tr 'A-F\n' 'a-f ') "
		for uow_m in "$@"; do
			case "$uow_have" in
			*" $uow_m "*) ;;
			*)
				if ! hostapd_cli -p "$UOW_RUN/hostapd" -i "$uow_if" $uow_acl ADD_MAC "$uow_m" | grep -q '^OK'; then
					uow_err="$uow_acl ADD_MAC failed on $uow_if"; return 1
				fi ;;
			esac
		done
		for uow_m in $uow_have; do
			case " $* " in *" $uow_m "*) continue ;; esac
			if [ "$uow_acl" = deny_acl ]; then
				case " $UOW_KEEP " in *" $uow_m "*) continue ;; esac
			fi
			if ! hostapd_cli -p "$UOW_RUN/hostapd" -i "$uow_if" $uow_acl DEL_MAC "$uow_m" | grep -q '^OK'; then
				uow_err="$uow_acl DEL_MAC failed on $uow_if"; return 1
			fi
		done
		if [ "$uow_file" = write ]; then
			for uow_m in "$@"; do echo "$uow_m"; done > "$UOW_RUN/hostapd-$uow_if.maclist" || { uow_err="cannot write $UOW_RUN/hostapd-$uow_if.maclist"; return 1; }
		else
			rm -f "$UOW_RUN/hostapd-$uow_if.maclist" || { uow_err="cannot remove $UOW_RUN/hostapd-$uow_if.maclist"; return 1; }
		fi
		uow_raw=$(hostapd_cli -p "$UOW_RUN/hostapd" -i "$uow_if" $uow_acl SHOW 2>/dev/null) || { uow_err="$uow_acl SHOW failed on $uow_if"; return 1; }
		if [ ${#uow_raw} -ge 4000 ]; then uow_err="$uow_acl list too long for hostapd_cli SHOW on $uow_if"; return 1; fi
		uow_now=" $(printf '%s\n' "$uow_raw" | cut -d' ' -f1 | tr 'A-F\n' 'a-f ') "
		for uow_m in "$@"; do
			case "$uow_now" in
			*" $uow_m "*) ;;
			*) uow_err="$uow_m missing from $uow_acl on $uow_if"; return 1 ;;
			esac
		done
		for uow_m in $uow_now; do
			case " $* " in *" $uow_m "*) continue ;; esac
			if [ "$uow_acl" = deny_acl ]; then
				case " $UOW_KEEP " in *" $uow_m "*) continue ;; esac
			fi
			uow_err="$uow_m still on $uow_acl on $uow_if"; return 1
		done
	done
	return 0
}
`

// liveBlock is the shell that replaces the Wi-Fi reload in a live apply. It
// changes hostapd's lists for every changed section (liveFuncs), never
// taking keep (the blocked clients) off a deny list. It prints "UOW-LIVE
// ok", or "UOW-LIVE reload <reason>" and runs reload. run is /var/run
// (tests: a directory of their own). MACs are lower-cased and checked, and
// section names checked, before anything is built.
func liveBlock(changed []translate.ACL, keep []string, run, reload string) (string, error) {
	keep, err := cleanMACs(keep)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(run, "'\n") {
		return "", fmt.Errorf("bad run directory %q", run)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "UOW_RUN='%s'\nUOW_KEEP='%s'\n", run, strings.Join(keep, " "))
	b.WriteString(liveFuncs)
	b.WriteString("uow_ok=1\ncommand -v hostapd_cli >/dev/null 2>&1 || { uow_ok=; uow_err='hostapd_cli is not installed'; }\ncommand -v jsonfilter >/dev/null 2>&1 || { [ -z \"$uow_ok\" ] || { uow_ok=; uow_err='jsonfilter is not installed'; }; }\n")
	for _, a := range changed {
		if !sectionOK.MatchString(a.Section) {
			return "", fmt.Errorf("bad section name %q", a.Section)
		}
		acl, file := "deny_acl", "write"
		switch a.Policy {
		case "":
			file = "delete"
		case "deny":
		case "allow":
			acl = "accept_acl"
		default:
			return "", fmt.Errorf("section %s: unknown MAC filter policy %q", a.Section, a.Policy)
		}
		macs, err := cleanMACs(a.MACs)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "if [ -n \"$uow_ok\" ] && ! uow_live %s %s %s %s; then uow_ok=; fi\n", a.Section, acl, file, strings.Join(macs, " "))
	}
	fmt.Fprintf(&b, "if [ -n \"$uow_ok\" ]; then echo 'UOW-LIVE ok'; else echo \"UOW-LIVE reload $uow_err\"; %s; fi\n", reload)
	return b.String(), nil
}

// cleanMACs lower-cases, checks and de-duplicates MACs, keeping their order.
func cleanMACs(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, m := range in {
		m = strings.ToLower(m)
		if !macOK.MatchString(m) {
			return nil, fmt.Errorf("bad MAC %q", m)
		}
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out, nil
}

// liveOutcome reads liveBlock's report from the apply output: "live", or
// "reload: <reason>".
func liveOutcome(out string) string {
	for _, l := range strings.Split(out, "\n") {
		r, ok := strings.CutPrefix(l, "UOW-LIVE ")
		if !ok {
			continue
		}
		if r == "ok" {
			return "live"
		}
		if why, ok := strings.CutPrefix(r, "reload "); ok {
			return "reload: " + why
		}
	}
	return "reload: no report from the live update"
}

// syncScript is the shell that brings hostapd's lists for acls in line, as
// liveBlock does, without a reload: it prints "UOW-LIVE ok" or "UOW-LIVE
// reload <reason>" and changes nothing else.
func syncScript(acls []translate.ACL, keep []string, run string) (string, error) {
	b, err := liveBlock(acls, keep, run, ":")
	if err != nil {
		return "", err
	}
	return "set -e\n" + b, nil
}

// syncOutcome turns syncScript's output into an error with the reason.
func syncOutcome(out string) error {
	if how := liveOutcome(out); how != "live" {
		return errors.New(strings.TrimPrefix(how, "reload: "))
	}
	return nil
}

// SyncACLs brings hostapd's MAC lists on every running interface of acls'
// sections in line with acls (keep stays on deny lists). Needed because
// netifd restarts a BSS on a reload only when its config differs from what
// netifd last loaded: after live changes, a reload that brings a list back
// to that state leaves hostapd's live-changed list in place.
func (cl *Client) SyncACLs(acls []translate.ACL, keep []string) error {
	if len(acls) == 0 {
		return nil
	}
	s, err := syncScript(acls, keep, "/var/run")
	if err != nil {
		return err
	}
	out, err := cl.Run("sh -c " + shQuote(s))
	if err != nil {
		return fmt.Errorf("%v (%s)", err, strings.TrimSpace(string(out)))
	}
	return syncOutcome(string(out))
}
