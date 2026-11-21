// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// liveEnv runs liveBlock's shell against stand-ins for ubus, jsonfilter,
// hostapd_cli, iw and wifi, with a PATH that holds nothing else but the few
// tools the shell needs.
type liveEnv struct {
	t     *testing.T
	path  string // PATH for the shell
	run   string // stands in for /var/run
	state string // the stand-ins' state: sections, lists, calls
}

// jsonfilter: the interfaces of the section in the expression, from
// $STATE/sections/<section>; none = exit 1 (as jsonfilter does).
const stubJSONFilter = `sec=${2#*section=\'}
sec=${sec%%\'*}
f="$STATE/sections/$sec"
[ -s "$f" ] || exit 1
cat "$f"
`

// hostapd_cli -p <dir> -i <ifname> <acl> SHOW|ADD_MAC|DEL_MAC [mac], with
// the lists in $STATE/lists/<ifname>.<acl>. With $STATE/broken-add,
// ADD_MAC answers OK and adds nothing; with $STATE/broken-show, SHOW
// exits 255 without output.
const stubHostapdCLI = `echo "hostapd_cli $*" >> "$STATE/calls"
while [ $# -gt 0 ]; do
	case "$1" in
	-p) shift 2 ;;
	-i) ifn=$2; shift 2 ;;
	*) break ;;
	esac
done
acl=$1 cmd=$2 mac=$3
f="$STATE/lists/$ifn.$acl"
touch "$f"
case "$cmd" in
SHOW) [ -e "$STATE/broken-show" ] && exit 255
	while read -r m; do [ -n "$m" ] && echo "$m VLAN_ID=0"; done < "$f" ;;
ADD_MAC) [ -e "$STATE/broken-add" ] || grep -qx "$mac" "$f" || echo "$mac" >> "$f"; echo OK ;;
DEL_MAC) grep -vx "$mac" "$f" > "$f.new"; mv "$f.new" "$f"; echo OK ;;
*) echo FAIL ;;
esac
`

// newLiveEnv: running maps a UCI section to its interfaces (each gets a
// control socket), lists holds hostapd's lists per "<ifname>.<acl>".
func newLiveEnv(t *testing.T, withCLI bool, running map[string][]string, lists map[string][]string) *liveEnv {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	// unix socket paths are limited (104 bytes on macOS): keep run short
	short, err := os.MkdirTemp("", "uow")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	e := &liveEnv{t: t, run: filepath.Join(short, "run"), state: filepath.Join(dir, "state")}
	bin, tools := filepath.Join(dir, "bin"), filepath.Join(dir, "tools")
	for _, d := range []string{bin, tools, filepath.Join(e.run, "hostapd"), filepath.Join(e.state, "sections"), filepath.Join(e.state, "lists")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range []string{"cut", "tr", "grep", "cat", "rm", "mv", "touch"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s", tool)
		}
		if err := os.Symlink(p, filepath.Join(tools, tool)); err != nil {
			t.Fatal(err)
		}
	}
	stubs := map[string]string{
		"ubus":       "echo '{}'\n",
		"jsonfilter": stubJSONFilter,
		"iw":         "exit 0\n",
		"wifi":       "echo \"wifi $*\" >> \"$STATE/calls\"\n",
	}
	if withCLI {
		stubs["hostapd_cli"] = stubHostapdCLI
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for sec, ifs := range running {
		if err := os.WriteFile(filepath.Join(e.state, "sections", sec), []byte(strings.Join(ifs, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, i := range ifs {
			l, err := net.Listen("unix", filepath.Join(e.run, "hostapd", i))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { l.Close() })
		}
	}
	for k, macs := range lists {
		if err := os.WriteFile(filepath.Join(e.state, "lists", k), []byte(strings.Join(macs, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.path = bin + ":" + tools
	return e
}

// exec runs the block under set -e and returns its output and the calls
// the stand-ins recorded.
func (e *liveEnv) exec(block string) (string, string) {
	e.t.Helper()
	c := exec.Command("sh", "-c", "set -e\n"+block)
	c.Env = []string{"PATH=" + e.path, "STATE=" + e.state}
	out, err := c.CombinedOutput()
	if err != nil {
		e.t.Fatalf("sh: %v\n%s", err, out)
	}
	calls, _ := os.ReadFile(filepath.Join(e.state, "calls"))
	return string(out), string(calls)
}

// list is hostapd's list "<ifname>.<acl>" after the run.
func (e *liveEnv) list(key string) []string {
	b, _ := os.ReadFile(filepath.Join(e.state, "lists", key))
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

const (
	macKeep = "02:00:00:00:00:0b" // a blocked client
	macOld  = "02:00:00:00:00:0c"
	macNew  = "02:00:00:00:00:0d"
)

func TestLiveBlockDeny(t *testing.T) {
	e := newLiveEnv(t, true, map[string][]string{"unifi_radio0_4": {"phy0-ap2"}},
		map[string][]string{"phy0-ap2.deny_acl": {macKeep, macOld}})
	b, err := liveBlock([]translate.ACL{{Section: "unifi_radio0_4", Policy: "deny", MACs: []string{macNew}}}, []string{macKeep}, e.run, "wifi reload")
	if err != nil {
		t.Fatal(err)
	}
	out, calls := e.exec(b)
	if !strings.Contains(out, "UOW-LIVE ok") {
		t.Fatalf("output:\n%s\ncalls:\n%s", out, calls)
	}
	if got := e.list("phy0-ap2.deny_acl"); !reflect.DeepEqual(got, []string{macKeep, macNew}) {
		t.Errorf("deny list %v", got)
	}
	if !strings.Contains(calls, "deny_acl ADD_MAC "+macNew) || !strings.Contains(calls, "deny_acl DEL_MAC "+macOld) ||
		strings.Contains(calls, "DEL_MAC "+macKeep) || strings.Contains(calls, "wifi") {
		t.Errorf("calls:\n%s", calls)
	}
	if f, _ := os.ReadFile(filepath.Join(e.run, "hostapd-phy0-ap2.maclist")); string(f) != macNew+"\n" {
		t.Errorf("maclist file %q", f)
	}
}

func TestLiveBlockAllowAndNone(t *testing.T) {
	a, b, c, x := "02:00:00:00:00:01", "02:00:00:00:00:02", "02:00:00:00:00:03", "02:00:00:00:00:09"
	e := newLiveEnv(t, true, map[string][]string{"s1": {"phy0-ap0"}, "s2": {"phy1-ap0"}},
		map[string][]string{"phy0-ap0.accept_acl": {a, b}, "phy1-ap0.deny_acl": {x}})
	if err := os.WriteFile(filepath.Join(e.run, "hostapd-phy1-ap0.maclist"), []byte(x+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blk, err := liveBlock([]translate.ACL{{Section: "s1", Policy: "allow", MACs: []string{b, c}}, {Section: "s2"}}, nil, e.run, "wifi reload")
	if err != nil {
		t.Fatal(err)
	}
	out, calls := e.exec(blk)
	if !strings.Contains(out, "UOW-LIVE ok") {
		t.Fatalf("output:\n%s\ncalls:\n%s", out, calls)
	}
	if got := e.list("phy0-ap0.accept_acl"); !reflect.DeepEqual(got, []string{b, c}) {
		t.Errorf("accept list %v", got)
	}
	if got := e.list("phy1-ap0.deny_acl"); len(got) != 0 {
		t.Errorf("deny list %v", got)
	}
	if f, _ := os.ReadFile(filepath.Join(e.run, "hostapd-phy0-ap0.maclist")); string(f) != b+"\n"+c+"\n" {
		t.Errorf("allow maclist file %q", f)
	}
	if _, err := os.Stat(filepath.Join(e.run, "hostapd-phy1-ap0.maclist")); !os.IsNotExist(err) {
		t.Errorf("maclist file of the unfiltered WLAN still there: %v", err)
	}
}

// A WLAN that is not running is left to hostapd's next start.
func TestLiveBlockNotRunning(t *testing.T) {
	e := newLiveEnv(t, true, nil, nil)
	b, err := liveBlock([]translate.ACL{{Section: "s1", Policy: "deny", MACs: []string{macNew}}}, nil, e.run, "wifi reload")
	if err != nil {
		t.Fatal(err)
	}
	out, calls := e.exec(b)
	if !strings.Contains(out, "UOW-LIVE ok") || calls != "" {
		t.Errorf("output:\n%s\ncalls:\n%s", out, calls)
	}
}

func TestLiveBlockFallback(t *testing.T) {
	acl := []translate.ACL{{Section: "s1", Policy: "deny", MACs: []string{macNew}}}
	for name, c := range map[string]struct {
		withCLI bool
		ifname  string
		prepare func(e *liveEnv)
		reason  string
	}{
		"no hostapd_cli": {false, "phy0-ap0", nil, "hostapd_cli is not installed"},
		"no control socket": {true, "phy0-ap0", func(e *liveEnv) {
			os.Remove(filepath.Join(e.run, "hostapd", "phy0-ap0"))
		}, "no hostapd control socket for phy0-ap0"},
		"adds nothing": {true, "phy0-ap0", func(e *liveEnv) {
			os.WriteFile(filepath.Join(e.state, "broken-add"), nil, 0o644)
		}, macNew + " missing from deny_acl on phy0-ap0"},
		"bad interface name": {true, "phy0;ap0", nil, "bad interface name for s1"},
		"name too long":      {true, "phy0-ap0-toolong", nil, "bad interface name for s1"},
		"glob in name":       {true, "phy*", nil, "bad interface name for s1"},
		"cannot write maclist": {true, "phy0-ap0", func(e *liveEnv) {
			os.Mkdir(filepath.Join(e.run, "hostapd-phy0-ap0.maclist"), 0o755)
		}, "cannot write"},
	} {
		e := newLiveEnv(t, c.withCLI, map[string][]string{"s1": {c.ifname}}, nil)
		if c.prepare != nil {
			c.prepare(e)
		}
		b, err := liveBlock(acl, nil, e.run, "wifi reload")
		if err != nil {
			t.Fatal(err)
		}
		out, calls := e.exec(b)
		if !strings.Contains(out, "UOW-LIVE reload "+c.reason) || !strings.Contains(calls, "wifi reload") {
			t.Errorf("%s: output:\n%s\ncalls:\n%s", name, out, calls)
		}
	}
}

func TestLiveBlockShowFails(t *testing.T) {
	e := newLiveEnv(t, true, map[string][]string{"s1": {"phy0-ap0"}}, map[string][]string{"phy0-ap0.deny_acl": {macOld}})
	os.WriteFile(filepath.Join(e.state, "broken-show"), nil, 0o644)
	b, err := liveBlock([]translate.ACL{{Section: "s1", Policy: ""}}, nil, e.run, "wifi reload")
	if err != nil {
		t.Fatal(err)
	}
	out, calls := e.exec(b)
	if !strings.Contains(out, "UOW-LIVE reload deny_acl SHOW failed on phy0-ap0") || !strings.Contains(calls, "wifi reload") {
		t.Errorf("output:\n%s\ncalls:\n%s", out, calls)
	}
}

// hostapd's control replies hold 4096 bytes: a longer list cannot be read
// back whole, so it is not changed live.
func TestLiveBlockLongList(t *testing.T) {
	var macs []string
	for i := 0; i < 150; i++ {
		macs = append(macs, fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256))
	}
	e := newLiveEnv(t, true, map[string][]string{"s1": {"phy0-ap0"}}, map[string][]string{"phy0-ap0.deny_acl": macs})
	b, err := liveBlock([]translate.ACL{{Section: "s1", Policy: "deny", MACs: macs}}, nil, e.run, "wifi reload")
	if err != nil {
		t.Fatal(err)
	}
	out, calls := e.exec(b)
	if !strings.Contains(out, "UOW-LIVE reload deny_acl list too long for hostapd_cli SHOW on phy0-ap0") || !strings.Contains(calls, "wifi reload") {
		t.Errorf("output:\n%s\ncalls:\n%s", out, calls)
	}
}

func TestLiveBlockNoJSONFilter(t *testing.T) {
	e := newLiveEnv(t, true, map[string][]string{"s1": {"phy0-ap0"}}, nil)
	if err := os.Remove(filepath.Join(strings.Split(e.path, ":")[0], "jsonfilter")); err != nil {
		t.Fatal(err)
	}
	b, err := liveBlock([]translate.ACL{{Section: "s1", Policy: "deny", MACs: []string{macNew}}}, nil, e.run, "wifi reload")
	if err != nil {
		t.Fatal(err)
	}
	out, calls := e.exec(b)
	if !strings.Contains(out, "UOW-LIVE reload jsonfilter is not installed") || !strings.Contains(calls, "wifi reload") {
		t.Errorf("output:\n%s\ncalls:\n%s", out, calls)
	}
}

// syncScript brings the lists in line without any reload.
func TestSyncScript(t *testing.T) {
	acl := []translate.ACL{{Section: "s1", Policy: "deny", MACs: []string{macNew}}}
	e := newLiveEnv(t, true, map[string][]string{"s1": {"phy0-ap0"}},
		map[string][]string{"phy0-ap0.deny_acl": {macKeep, macOld}})
	s, err := syncScript(acl, []string{macKeep}, e.run)
	if err != nil {
		t.Fatal(err)
	}
	out, calls := e.exec(s)
	if !strings.Contains(out, "UOW-LIVE ok") || strings.Contains(calls, "wifi") {
		t.Errorf("output:\n%s\ncalls:\n%s", out, calls)
	}
	got := e.list("phy0-ap0.deny_acl")
	sort.Strings(got)
	want := []string{macKeep, macNew}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list %v, want %v", got, want)
	}

	e = newLiveEnv(t, false, map[string][]string{"s1": {"phy0-ap0"}}, nil)
	s, err = syncScript(acl, nil, e.run)
	if err != nil {
		t.Fatal(err)
	}
	out, calls = e.exec(s)
	if !strings.Contains(out, "UOW-LIVE reload hostapd_cli is not installed") || strings.Contains(calls, "wifi") {
		t.Errorf("output:\n%s\ncalls:\n%s", out, calls)
	}
}
