// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// VLANs (docs/internals.md). For a WLAN on a tagged network the UniFi sends a
// second bridge named after the VLAN, holding the WLAN's VAPs and the tagged
// uplink, with no address for the AP:
//
//	vlan.1.devname=eth0  vlan.1.id=77
//	bridge.2.devname=br0.77
//	bridge.2.port.1.devname=wifi0ap5   (the VAP)
//	bridge.2.port.3.devname=eth0.77    (the cable, tagged)
//	netconf.8.devname=br0.77  netconf.8.ip=0.0.0.0
//
// OpenWrt (Wi-Fi-only VLANs, no switch port VLANs): a bridge br-uow<vid> over
// <uplink port>.<vid> (an 802.1Q device on the cable port; br-lan stays as it
// is), an interface uow<vid> with proto none (the AP has no address there: no
// DHCP, DNS or RA from it), and the WLAN's wifi-ifaces get network=uow<vid>.
//
// In-wall switch ports (switch.*):
//   - Disabled ports (switch.port.<p>.status=disabled or opmode=disabled, even
//     when switch.status=disabled) are removed from br-lan (and restored when
//     re-enabled).
//   - When switch.status=enabled and switch.vlan.status=enabled, splitting two
//     ports of the same DSA switch chip across br-lan and br-uow<vid> breaks
//     hardware ARL/FDB unicast forwarding on DSA switches (e.g. qca8k). Instead
//     br-lan uses OpenWrt's bridge-vlan (vlan_filtering=1), network.lan moves to
//     br-lan.1, wired port VLANs are configured via network.uowbv<vid>
//     (bridge-vlan) sections directly on br-lan, and Wi-Fi VLAN bridges
//     (br-uow<vid>) attach to br-lan.<vid>. The uplink port is never removed
//     from br-lan or VLAN 1.

var vlanBridge = regexp.MustCompile(`\.(\d{1,4})$`)

func validVLANID(id string) bool {
	n, err := strconv.Atoi(id)
	return err == nil && n >= 1 && n <= 4094 && strconv.Itoa(n) == id
}

// vlanNetworks: VAP devname -> OpenWrt network name, and the VLAN ids in use.
func vlanNetworks(cfg Cfg) (map[string]string, []string) {
	vaps := map[string]bool{}
	for _, n := range indices(cfg, "wireless") {
		if d := cfg["wireless."+n+".devname"]; d != "" {
			vaps[d] = true
		}
	}
	netOf := map[string]string{}
	seen := map[string]bool{}
	for _, b := range indices(cfg, "bridge") {
		m := vlanBridge.FindStringSubmatch(cfg["bridge."+b+".devname"])
		if m == nil || !validVLANID(m[1]) {
			continue
		}
		id := m[1]
		for _, p := range indices(cfg, "bridge."+b+".port") {
			d := cfg["bridge."+b+".port."+p+".devname"]
			if vaps[d] {
				netOf[d] = "uow" + id
				seen[id] = true
			}
		}
	}
	var ids []string
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return netOf, ids
}

type switchVLAN struct {
	key string
	vid string
}

type switchPlan struct {
	vlanOn        bool
	extraVLANs    []string
	bvMembers     map[string][]string // vid -> bridge-vlan port specs (e.g. "lan:u*", "lan:t")
	disabledPorts map[string]bool     // downlink ports to remove from br-lan
	excludeVLAN1  map[string]bool     // downlink ports excluded from native VLAN 1
	notes         []string
}

// switchPortPlan parses switch.* for downlink Ethernet ports.
func switchPortPlan(cfg Cfg, uplinkPort string, switchPorts map[int]string) switchPlan {
	sp := switchPlan{
		vlanOn:        cfg["switch.status"] == "enabled" && cfg["switch.vlan.status"] == "enabled",
		bvMembers:     map[string][]string{},
		disabledPorts: map[string]bool{},
		excludeVLAN1:  map[string]bool{},
	}
	if len(switchPorts) == 0 {
		if sp.vlanOn {
			sp.notes = append(sp.notes, "switch: port VLAN enabled in controller, but no downlink ports are known: skipped")
		}
		return sp
	}

	var vlans []switchVLAN
	vlan1Disabled := false
	if sp.vlanOn {
		for _, k := range indices(cfg, "switch.vlan") {
			id := cfg["switch.vlan."+k+".id"]
			if !validVLANID(id) {
				continue
			}
			if cfg["switch.vlan."+k+".status"] == "disabled" {
				if id == "1" {
					vlan1Disabled = true
				}
				continue
			}
			vlans = append(vlans, switchVLAN{key: k, vid: id})
		}
	}

	seenVLAN := map[string]bool{}
	seenPortVLAN := map[string]bool{}
	for _, idx := range slices.Sorted(maps.Keys(switchPorts)) {
		ifname := switchPorts[idx]
		if ifname == "" || ifname == uplinkPort {
			continue
		}
		p := strconv.Itoa(idx)
		if cfg["switch.port."+p+".status"] == "disabled" || cfg["switch.port."+p+".opmode"] == "disabled" {
			sp.disabledPorts[ifname] = true
			sp.excludeVLAN1[ifname] = true
			sp.notes = append(sp.notes, fmt.Sprintf("switch port %d (%s): disabled (removed from br-lan)", idx, ifname))
			continue
		}
		if !sp.vlanOn {
			continue
		}
		if vlan1Disabled {
			sp.excludeVLAN1[ifname] = true
		}
		for _, v := range vlans {
			mode := cfg["switch.vlan."+v.key+".port."+p+".mode"]
			if mode == "" {
				continue
			}
			if v.vid == "1" {
				switch mode {
				case "exclude":
					sp.excludeVLAN1[ifname] = true
				case "tagged":
					sp.notes = append(sp.notes, fmt.Sprintf("switch port %d (%s): tagged vlan 1 not supported (kept untagged on br-lan)", idx, ifname))
				}
				continue
			}
			pv := ifname + "|" + v.vid
			if seenPortVLAN[pv] {
				continue
			}
			switch mode {
			case "untagged":
				seenPortVLAN[pv] = true
				sp.excludeVLAN1[ifname] = true
				seenVLAN[v.vid] = true
				sp.bvMembers[v.vid] = append(sp.bvMembers[v.vid], ifname+":u*")
				sp.notes = append(sp.notes, fmt.Sprintf("switch port %d (%s): native vlan %s (bridge-vlan on br-lan)", idx, ifname, v.vid))
			case "tagged":
				seenPortVLAN[pv] = true
				seenVLAN[v.vid] = true
				sp.bvMembers[v.vid] = append(sp.bvMembers[v.vid], ifname+":t")
				sp.notes = append(sp.notes, fmt.Sprintf("switch port %d (%s): tagged vlan %s (bridge-vlan on br-lan)", idx, ifname, v.vid))
			}
		}
	}
	sp.extraVLANs = slices.Sorted(maps.Keys(seenVLAN))
	return sp
}

// planVLAN creates or removes per-VLAN bridges/interfaces and, when switch port
// VLANs are active, configures OpenWrt bridge-vlan (vlan_filtering=1) on br-lan.
func planVLAN(cfg Cfg, cur UCI, uplinkPort string, switchPorts map[int]string, r *Result) []Op {
	_, wifiIDs := vlanNetworks(cfg)

	// Locate br-lan device section and its current ports (sorted keys for deterministic selection).
	brKey, brPorts := "", []string(nil)
	for _, k := range slices.Sorted(maps.Keys(cur)) {
		if strings.HasPrefix(k, "network.") && strings.HasSuffix(k, ".name") && cur[k] == "br-lan" {
			sec := strings.TrimSuffix(k, ".name")
			if cur[sec] != "device" {
				continue
			}
			brKey = sec
			brPorts = strings.Fields(strings.ReplaceAll(cur[brKey+".ports"], "'", ""))
			break
		}
	}
	// Cable port: explicit or detected uplinkPort when set, else first port of br-lan.
	port := uplinkPort
	if port == "" && len(brPorts) > 0 {
		port = brPorts[0]
	}

	sp := switchPortPlan(cfg, port, switchPorts)
	r.Notes = append(r.Notes, sp.notes...)

	// Ports referenced by another UCI interface or non-uow device are unmanaged.
	usedElsewhere := map[string]bool{}
	markUsed := func(dev string) {
		if dev == "" {
			return
		}
		usedElsewhere[dev] = true
		if base, _, ok := strings.Cut(dev, "."); ok && base != "" {
			usedElsewhere[base] = true
		}
	}
	var existingUowBV []string
	hasOtherBV := false
	inUowVLAN := map[string]bool{}
	for k, v := range cur {
		s, ok := strings.CutPrefix(k, "network.")
		if !ok {
			continue
		}
		if !strings.Contains(s, ".") && v == "bridge-vlan" {
			if isUowVLAN(s, "uowbv") {
				existingUowBV = append(existingUowBV, k)
			} else if cur[k+".device"] == "br-lan" {
				hasOtherBV = true
			}
			continue
		}
		sec, opt, ok := strings.Cut(s, ".")
		if !ok {
			continue
		}
		if opt == "ports" && cur["network."+sec] == "device" && isUowVLAN(sec, "uowvlan") {
			for _, mp := range strings.Fields(strings.ReplaceAll(v, "'", "")) {
				if !strings.Contains(mp, ".") {
					inUowVLAN[mp] = true
				}
			}
			continue
		}
		if sec == "lan" || "network."+sec == brKey || isUowVLAN(sec, "uowvlan") || isUowVLAN(sec, "uow") || isUowVLAN(sec, "uowbv") {
			continue
		}
		typ := cur["network."+sec]
		if (typ == "interface" && (opt == "device" || opt == "ifname")) ||
			(typ == "device" && (opt == "ports" || opt == "ifname")) {
			for _, mp := range strings.Fields(strings.ReplaceAll(v, "'", "")) {
				markUsed(mp)
			}
		}
	}

	hasUowBV := len(existingUowBV) > 0
	// Use bridge-vlan on br-lan whenever Port VLAN is enabled and we know br-lan + uplink (in br-lan) + downlink ports.
	uplinkInBr := port != "" && slices.Contains(brPorts, port)
	if sp.vlanOn && brKey != "" && port != "" && len(switchPorts) > 0 && !uplinkInBr {
		r.Notes = append(r.Notes, fmt.Sprintf("switch: uplink port %s is not in br-lan: bridge-vlan skipped", port))
	}
	useBridgeVLAN := sp.vlanOn && brKey != "" && uplinkInBr && len(switchPorts) > 0
	keepBridgeVLAN := useBridgeVLAN || (sp.vlanOn && hasUowBV)

	var ops []Op
	want := map[string]bool{}

	// Compute desired br-lan ports (removing only disabled downlink ports; restoring active managed ones).
	wantBr := append([]string(nil), brPorts...)
	if brKey != "" && len(switchPorts) > 0 {
		wantBr = nil
		for _, p := range brPorts {
			if p != port && sp.disabledPorts[p] {
				continue
			}
			wantBr = append(wantBr, p)
		}
		for _, idx := range slices.Sorted(maps.Keys(switchPorts)) {
			ifname := switchPorts[idx]
			if ifname == "" || ifname == port || sp.disabledPorts[ifname] {
				continue
			}
			managed := inUowVLAN[ifname] || (cfg["switch.port."+strconv.Itoa(idx)+".opmode"] == "switch" && !usedElsewhere[ifname])
			if managed && !slices.Contains(wantBr, ifname) {
				wantBr = append(wantBr, ifname)
			}
		}
		if len(wantBr) > 0 && (port == "" || slices.Contains(wantBr, port)) && !slices.Equal(brPorts, wantBr) {
			ops = append(ops, Op{Key: brKey + ".ports", List: wantBr, Old: cur[brKey+".ports"]})
		}
	}

	if useBridgeVLAN {
		// Enable vlan_filtering on br-lan and point network.lan to br-lan.1.
		if cur[brKey+".vlan_filtering"] != "1" {
			ops = append(ops, Op{Key: brKey + ".vlan_filtering", Value: "1", Old: cur[brKey+".vlan_filtering"]})
		}
		if cur["network.lan"] == "interface" &&
			(cur["network.lan.device"] == "br-lan" || (cur["network.lan.device"] == "" && cur["network.lan.ifname"] == "")) {
			ops = append(ops, Op{Key: "network.lan.device", Value: "br-lan.1", Old: cur["network.lan.device"]})
		}

		// Native VLAN 1 bridge-vlan: uplink + any active br-lan downlink port not excluded from VLAN 1.
		bv1Ports := []string{port + ":u*"}
		for _, p := range wantBr {
			if p == port || sp.disabledPorts[p] || sp.excludeVLAN1[p] {
				continue
			}
			bv1Ports = append(bv1Ports, p+":u*")
		}
		ops = append(ops, planBridgeVLAN("1", bv1Ports, cur, want)...)

		// Non-default VLANs: union of Wi-Fi VLANs and switch-port VLANs whose ports are on br-lan.
		wifiSet := map[string]bool{}
		bvIDSet := map[string]bool{}
		for _, id := range wifiIDs {
			wifiSet[id] = true
			bvIDSet[id] = true
		}
		for _, id := range sp.extraVLANs {
			bvIDSet[id] = true
		}
		for _, id := range slices.Sorted(maps.Keys(bvIDSet)) {
			if id == "1" {
				continue
			}
			var downlinks []string
			for _, m := range sp.bvMembers[id] {
				ifname, _, _ := strings.Cut(m, ":")
				if !slices.Contains(wantBr, ifname) {
					continue
				}
				if !slices.Contains(downlinks, m) {
					downlinks = append(downlinks, m)
				}
			}
			if len(downlinks) == 0 && !wifiSet[id] {
				continue
			}
			bvPorts := append([]string{port + ":t"}, downlinks...)
			ops = append(ops, planBridgeVLAN(id, bvPorts, cur, want)...)
		}
	} else if sp.vlanOn && hasUowBV {
		// Port VLAN is still enabled, but switchPorts or port is missing: keep existing bridge-vlan intact.
		for _, k := range existingUowBV {
			want[k] = true
		}
	} else if !sp.vlanOn && hasUowBV && !hasOtherBV && brKey != "" {
		// Reverting from bridge-vlan back to plain br-lan.
		if old, ok := cur[brKey+".vlan_filtering"]; ok {
			ops = append(ops, Op{Del: true, Key: brKey + ".vlan_filtering", Old: old})
		}
		if cur["network.lan"] == "interface" && cur["network.lan.device"] == "br-lan.1" {
			ops = append(ops, Op{Key: "network.lan.device", Value: "br-lan", Old: "br-lan.1"})
		}
	}

	// Wi-Fi VLAN bridges (only needed for VLANs carrying Wi-Fi SSIDs).
	// Under bridge-vlan they attach to br-lan.<vid>; otherwise to <uplink>.<vid>.
	for _, id := range wifiIDs {
		if port == "" {
			r.Notes = append(r.Notes, fmt.Sprintf("vlan %s: no br-lan port found in the network config: not set up", id))
			continue
		}
		dev, ifc := "network.uowvlan"+id, "network.uow"+id
		want[dev], want[ifc] = true, true
		parent := port + "." + id
		if keepBridgeVLAN {
			parent = "br-lan." + id
		}
		wantPorts := []string{parent}
		if cur[dev] != "device" {
			ops = append(ops, Op{Key: dev, Value: "device"})
			r.Notes = append(r.Notes, fmt.Sprintf("vlan %s: bridge br-uow%s over %s, no address on the AP", id, id, parent))
		}
		for _, kv := range [][2]string{{"name", "br-uow" + id}, {"type", "bridge"}} {
			if cur[dev+"."+kv[0]] != kv[1] {
				ops = append(ops, Op{Key: dev + "." + kv[0], Value: kv[1], Old: cur[dev+"."+kv[0]]})
			}
		}
		curPorts := strings.Fields(strings.ReplaceAll(cur[dev+".ports"], "'", ""))
		if !slices.Equal(curPorts, wantPorts) {
			ops = append(ops, Op{Key: dev + ".ports", List: wantPorts, Old: cur[dev+".ports"]})
		}
		if cur[ifc] != "interface" {
			ops = append(ops, Op{Key: ifc, Value: "interface"})
		}
		for _, kv := range [][2]string{{"device", "br-uow" + id}, {"proto", "none"}} {
			if cur[ifc+"."+kv[0]] != kv[1] {
				ops = append(ops, Op{Key: ifc + "." + kv[0], Value: kv[1], Old: cur[ifc+"."+kv[0]]})
			}
		}
	}

	// Remove stale uow* sections we created for VLANs that are gone.
	var stale []string
	for k, v := range cur {
		s, ok := strings.CutPrefix(k, "network.")
		if !ok || strings.Contains(s, ".") || want[k] {
			continue
		}
		if (v == "interface" && isUowVLAN(s, "uow")) ||
			(v == "device" && isUowVLAN(s, "uowvlan")) ||
			(v == "bridge-vlan" && isUowVLAN(s, "uowbv")) {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		ops = append(ops, Op{Del: true, Key: k, Old: cur[k]})
	}
	return ops
}

func planBridgeVLAN(vid string, ports []string, cur UCI, want map[string]bool) []Op {
	sec := "network.uowbv" + vid
	want[sec] = true
	var ops []Op
	if cur[sec] != "bridge-vlan" {
		ops = append(ops, Op{Key: sec, Value: "bridge-vlan"})
	}
	for _, kv := range [][2]string{{"device", "br-lan"}, {"vlan", vid}} {
		if cur[sec+"."+kv[0]] != kv[1] {
			ops = append(ops, Op{Key: sec + "." + kv[0], Value: kv[1], Old: cur[sec+"."+kv[0]]})
		}
	}
	curPorts := strings.Fields(strings.ReplaceAll(cur[sec+".ports"], "'", ""))
	if !slices.Equal(curPorts, ports) {
		ops = append(ops, Op{Key: sec + ".ports", List: ports, Old: cur[sec+".ports"]})
	}
	return ops
}

func isUowVLAN(name, prefix string) bool {
	rest, ok := strings.CutPrefix(name, prefix)
	if !ok || rest == "" {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}
