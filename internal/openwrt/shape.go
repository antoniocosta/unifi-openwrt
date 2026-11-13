// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"sort"
	"strings"
)

// IfShape is per-client shaping on one AP interface: every station gets its
// own HTB class at the limit (fq_codel inside), like a UniFi AP's user-group
// rate limit. Download = the interface's egress; upload = its ingress,
// redirected through an IFB device.
type IfShape struct {
	Ifname           string
	DownKbps, UpKbps int
	Stations         []string // MACs, lower-case
}

// HasTC reports whether the AP can shape (tc + IFB available).
func (cl *Client) HasTC() bool {
	out, _ := cl.Run(`command -v tc >/dev/null && [ -d /sys/module/ifb ] || modprobe ifb 2>/dev/null; command -v tc >/dev/null && ip link add uow-ifbtest type ifb 2>/dev/null && ip link del uow-ifbtest && echo yes`)
	return strings.TrimSpace(string(out)) == "yes"
}

func ifbName(ifname string) string {
	n := "ifb-" + ifname
	if len(n) > 15 {
		n = n[:15]
	}
	return n
}

// shapeScript renders the tc commands for one interface (idempotent: it
// clears what it set before).
func shapeScript(s IfShape) (string, error) {
	if !ifnameOK.MatchString(s.Ifname) {
		return "", fmt.Errorf("bad interface %q", s.Ifname)
	}
	ifb := ifbName(s.Ifname)
	var b strings.Builder
	fmt.Fprintf(&b, "tc qdisc del dev %[1]s root 2>/dev/null; tc qdisc del dev %[1]s ingress 2>/dev/null; ip link del %[2]s 2>/dev/null\n", s.Ifname, ifb)
	macs := append([]string{}, s.Stations...)
	sort.Strings(macs)
	for _, m := range macs {
		if !macOK.MatchString(m) {
			return "", fmt.Errorf("bad MAC %q", m)
		}
	}
	tree := func(dev, dir string, kbps int) {
		fmt.Fprintf(&b, "tc qdisc add dev %s root handle 1: htb default 1\n", dev)
		fmt.Fprintf(&b, "tc class add dev %s parent 1: classid 1:1 htb rate 1000mbit quantum 1514\n", dev)
		fmt.Fprintf(&b, "tc qdisc add dev %s parent 1:1 fq_codel\n", dev)
		for i, m := range macs {
			id := 16 + i
			fmt.Fprintf(&b, "tc class add dev %s parent 1: classid 1:%x htb rate %dkbit ceil %dkbit quantum 1514\n", dev, id, kbps, kbps)
			fmt.Fprintf(&b, "tc qdisc add dev %s parent 1:%x fq_codel\n", dev, id)
			fmt.Fprintf(&b, "tc filter add dev %s parent 1: protocol all prio 1 u32 match ether %s %s classid 1:%x\n", dev, dir, m, id)
		}
	}
	if s.DownKbps > 0 {
		tree(s.Ifname, "dst", s.DownKbps)
	}
	if s.UpKbps > 0 {
		fmt.Fprintf(&b, "ip link add %[1]s type ifb && ip link set %[1]s up\n", ifb)
		fmt.Fprintf(&b, "tc qdisc add dev %s handle ffff: ingress\n", s.Ifname)
		fmt.Fprintf(&b, "tc filter add dev %s parent ffff: protocol all prio 1 u32 match u32 0 0 action mirred egress redirect dev %s\n", s.Ifname, ifb)
		tree(ifb, "src", s.UpKbps)
	}
	return b.String(), nil
}

// SetShaping applies shaping for the given interfaces and removes it from
// interfaces listed in clear.
func (cl *Client) SetShaping(ss []IfShape, clear []string) error {
	var b strings.Builder
	for _, c := range clear {
		if !ifnameOK.MatchString(c) {
			continue
		}
		fmt.Fprintf(&b, "tc qdisc del dev %[1]s root 2>/dev/null; tc qdisc del dev %[1]s ingress 2>/dev/null; ip link del %[2]s 2>/dev/null\n", c, ifbName(c))
	}
	for _, s := range ss {
		sc, err := shapeScript(s)
		if err != nil {
			return err
		}
		b.WriteString(sc)
	}
	b.WriteString("true\n")
	if out, err := cl.RunStdin("sh -s", []byte(b.String())); err != nil {
		return fmt.Errorf("shaping: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
