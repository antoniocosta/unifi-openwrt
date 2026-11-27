// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"slices"
	"strings"
	"testing"
)

func TestVLAN(t *testing.T) {
	cfg := Cfg{
		"wireless.1.devname": "wifi0ap0", "wireless.1.ssid": "home",
		"wireless.6.devname": "wifi0ap5", "wireless.6.ssid": "test",
		"bridge.1.devname": "br0", "bridge.1.port.1.devname": "eth0", "bridge.1.port.2.devname": "wifi0ap0",
		"bridge.2.devname": "br0.77", "bridge.2.port.1.devname": "wifi0ap5", "bridge.2.port.3.devname": "eth0.77",
	}
	netOf, ids := vlanNetworks(cfg)
	if netOf["wifi0ap5"] != "uow77" || netOf["wifi0ap0"] != "" || len(ids) != 1 || ids[0] != "77" {
		t.Fatalf("vlanNetworks: %v %v", netOf, ids)
	}
	cur := UCI{"network.cfg030f15": "device", "network.cfg030f15.name": "br-lan", "network.cfg030f15.ports": "lan1",
		"network.lan": "interface", "network.uow12": "interface", "network.uowvlan12": "device", "network.uowvlan12.name": "br-uow12"}
	var r Result
	got := map[string]string{}
	for _, o := range planVLAN(cfg, cur, "", nil, &r) {
		switch {
		case o.Del:
			got[o.Key] = "<del>"
		case o.List != nil:
			got[o.Key] = o.List[0]
		default:
			got[o.Key] = o.Value
		}
	}
	want := map[string]string{
		"network.uowvlan77": "device", "network.uowvlan77.name": "br-uow77", "network.uowvlan77.type": "bridge",
		"network.uowvlan77.ports": "lan1.77", "network.uow77": "interface", "network.uow77.device": "br-uow77",
		"network.uow77.proto": "none", "network.uow12": "<del>", "network.uowvlan12": "<del>",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("ops: %v", got)
	}
	if _, ok := got["network.lan"]; ok {
		t.Error("network.lan must not be touched")
	}
	// nothing to do once it is in place
	cur2 := UCI{"network.d": "device", "network.d.name": "br-lan", "network.d.ports": "lan1",
		"network.uowvlan77": "device", "network.uowvlan77.name": "br-uow77", "network.uowvlan77.type": "bridge",
		"network.uowvlan77.ports": "lan1.77", "network.uow77": "interface", "network.uow77.device": "br-uow77", "network.uow77.proto": "none"}
	if ops := planVLAN(cfg, cur2, "", nil, &r); len(ops) != 0 {
		t.Errorf("idempotent: %v", ops)
	}
}

func TestVLANUplinkPort(t *testing.T) {
	cfg := Cfg{
		"wireless.6.devname": "wifi0ap5", "wireless.6.ssid": "test",
		"bridge.2.devname": "br0.77", "bridge.2.port.1.devname": "wifi0ap5", "bridge.2.port.3.devname": "eth0.77",
	}
	cur := UCI{"network.d": "device", "network.d.name": "br-lan", "network.d.ports": "lan wan"}
	var r Result
	got := ""
	for _, o := range planVLAN(cfg, cur, "wan", nil, &r) {
		if o.Key == "network.uowvlan77.ports" && len(o.List) == 1 {
			got = o.List[0]
		}
	}
	if got != "wan.77" {
		t.Fatalf("vlan port = %q, want %q", got, "wan.77")
	}
}

type vlanPlan struct {
	vals  map[string]string
	lists map[string][]string
	dels  map[string]bool
	notes []string
	ops   []Op
}

func runPlanVLAN(cfg Cfg, cur UCI, uplink string, ports map[int]string) vlanPlan {
	p := vlanPlan{vals: map[string]string{}, lists: map[string][]string{}, dels: map[string]bool{}}
	var r Result
	p.ops = planVLAN(cfg, cur, uplink, ports, &r)
	p.notes = r.Notes
	for _, o := range p.ops {
		switch {
		case o.Del:
			p.dels[o.Key] = true
		case o.List != nil:
			p.lists[o.Key] = o.List
		default:
			p.vals[o.Key] = o.Value
		}
	}
	return p
}

func (p vlanPlan) hasNote(sub string) bool {
	return slices.ContainsFunc(p.notes, func(s string) bool { return strings.Contains(s, sub) })
}

func brLanUCI(ports string) UCI {
	return UCI{
		"network.d": "device", "network.d.name": "br-lan", "network.d.ports": ports,
		"network.lan": "interface", "network.lan.device": "br-lan",
	}
}

func TestVLANSwitchBridgeVLANAccessPort(t *testing.T) {
	// Access port on VLAN 40 alongside a Wi-Fi SSID on VLAN 20.
	cfg := Cfg{
		"wireless.1.devname": "wifi0ap1", "wireless.1.ssid": "home",
		"bridge.2.devname": "br0.20", "bridge.2.port.1.devname": "wifi0ap1", "bridge.2.port.2.devname": "eth0.20",
		"switch.status": "enabled", "switch.vlan.status": "enabled",
		"switch.vlan.1.id": "1", "switch.vlan.1.mode": "untagged", "switch.vlan.1.status": "enabled",
		"switch.vlan.4.id": "40", "switch.vlan.4.mode": "tagged", "switch.vlan.4.status": "enabled",
		"switch.port.1.opmode": "switch", "switch.port.1.pvid": "40",
		"switch.vlan.1.port.1.mode": "exclude", "switch.vlan.4.port.1.mode": "untagged",
	}
	ports := map[int]string{1: "lan"}
	p := runPlanVLAN(cfg, brLanUCI("wan lan"), "wan", ports)
	if p.vals["network.d.vlan_filtering"] != "1" || p.vals["network.lan.device"] != "br-lan.1" {
		t.Fatalf("br-lan setup = %v", p.vals)
	}
	for k, want := range map[string][]string{
		"network.uowbv1.ports":    {"wan:u*"},
		"network.uowbv20.ports":   {"wan:t"},
		"network.uowbv40.ports":   {"wan:t", "lan:u*"},
		"network.uowvlan20.ports": {"br-lan.20"},
	} {
		if !slices.Equal(p.lists[k], want) {
			t.Fatalf("%s = %v, want %v", k, p.lists[k], want)
		}
	}
	if _, exists := p.vals["network.uow40"]; exists {
		t.Fatalf("wired-only VLAN 40 should not create br-uow40 interface")
	}

	curApplied := UCI{
		"network.d": "device", "network.d.name": "br-lan", "network.d.ports": "wan lan", "network.d.vlan_filtering": "1",
		"network.lan": "interface", "network.lan.device": "br-lan.1", "network.lan.proto": "dhcp",
		"network.uowbv1": "bridge-vlan", "network.uowbv1.device": "br-lan", "network.uowbv1.vlan": "1", "network.uowbv1.ports": "'wan:u*'",
		"network.uowbv20": "bridge-vlan", "network.uowbv20.device": "br-lan", "network.uowbv20.vlan": "20", "network.uowbv20.ports": "'wan:t'",
		"network.uowbv40": "bridge-vlan", "network.uowbv40.device": "br-lan", "network.uowbv40.vlan": "40", "network.uowbv40.ports": "'wan:t' 'lan:u*'",
		"network.uowvlan20": "device", "network.uowvlan20.name": "br-uow20", "network.uowvlan20.type": "bridge", "network.uowvlan20.ports": "'br-lan.20'",
		"network.uow20": "interface", "network.uow20.device": "br-uow20", "network.uow20.proto": "none",
	}
	if got := runPlanVLAN(cfg, curApplied, "wan", ports); len(got.ops) != 0 {
		t.Fatalf("expected 0 ops when idempotent, got %v", got.ops)
	}

	// Reverting Port 1 to native VLAN 1 moves lan:u* to uowbv1 and deletes uowbv40.
	cfgNative1 := Cfg{
		"wireless.1.devname": "wifi0ap1", "wireless.1.ssid": "home",
		"bridge.2.devname": "br0.20", "bridge.2.port.1.devname": "wifi0ap1", "bridge.2.port.2.devname": "eth0.20",
		"switch.status": "enabled", "switch.vlan.status": "enabled",
		"switch.vlan.1.id": "1", "switch.vlan.1.status": "enabled", "switch.vlan.1.port.1.mode": "untagged",
		"switch.vlan.4.id": "40", "switch.vlan.4.status": "enabled", "switch.vlan.4.port.1.mode": "exclude",
		"switch.port.1.opmode": "switch",
	}
	p = runPlanVLAN(cfgNative1, curApplied, "wan", ports)
	if want := []string{"wan:u*", "lan:u*"}; !slices.Equal(p.lists["network.uowbv1.ports"], want) || !p.dels["network.uowbv40"] {
		t.Fatalf("native VLAN 1 revert: lists=%v dels=%v", p.lists, p.dels)
	}

	// Disabling switch.vlan.status reverts br-lan and switches Wi-Fi back to wan.20.
	cfgOff := Cfg{
		"wireless.1.devname": "wifi0ap1", "wireless.1.ssid": "home",
		"bridge.2.devname": "br0.20", "bridge.2.port.1.devname": "wifi0ap1", "bridge.2.port.2.devname": "eth0.20",
		"switch.status": "disabled", "switch.vlan.status": "disabled",
	}
	p = runPlanVLAN(cfgOff, curApplied, "wan", ports)
	if !p.dels["network.d.vlan_filtering"] || !p.dels["network.uowbv1"] || !p.dels["network.uowbv20"] || !p.dels["network.uowbv40"] {
		t.Fatalf("expected vlan_filtering and uowbv* deleted, got %v", p.dels)
	}
	if p.vals["network.lan.device"] != "br-lan" || !slices.Equal(p.lists["network.uowvlan20.ports"], []string{"wan.20"}) {
		t.Fatalf("revert state: vals=%v lists=%v", p.vals, p.lists)
	}
}

func TestVLANSwitchHybridAndHotMigration(t *testing.T) {
	ports := map[int]string{1: "lan"}
	t.Run("hybrid tagged port", func(t *testing.T) {
		cfg := Cfg{
			"switch.status": "enabled", "switch.vlan.status": "enabled",
			"switch.vlan.1.id": "1", "switch.vlan.1.status": "enabled", "switch.vlan.1.port.1.mode": "exclude",
			"switch.vlan.2.id": "20", "switch.vlan.2.status": "enabled", "switch.vlan.2.port.1.mode": "untagged",
			"switch.vlan.3.id": "30", "switch.vlan.3.status": "enabled", "switch.vlan.3.port.1.mode": "tagged",
			"switch.vlan.4.id": "40", "switch.vlan.4.status": "enabled", "switch.vlan.4.port.1.mode": "tagged",
			"switch.port.1.opmode": "switch",
		}
		p := runPlanVLAN(cfg, brLanUCI("wan lan"), "wan", ports)
		for k, want := range map[string][]string{
			"network.uowbv1.ports":  {"wan:u*"},
			"network.uowbv20.ports": {"wan:t", "lan:u*"},
			"network.uowbv30.ports": {"wan:t", "lan:t"},
			"network.uowbv40.ports": {"wan:t", "lan:t"},
		} {
			if !slices.Equal(p.lists[k], want) {
				t.Fatalf("%s = %v, want %v", k, p.lists[k], want)
			}
		}
	})

	t.Run("shared wifi and hot migration", func(t *testing.T) {
		cfg := Cfg{
			"wireless.1.devname": "wifi0ap1", "wireless.1.ssid": "iot",
			"bridge.2.devname": "br0.20", "bridge.2.port.1.devname": "wifi0ap1", "bridge.2.port.2.devname": "eth0.20",
			"switch.status": "enabled", "switch.vlan.status": "enabled",
			"switch.vlan.1.id": "1", "switch.vlan.1.status": "enabled", "switch.vlan.1.port.1.mode": "exclude",
			"switch.vlan.2.id": "20", "switch.vlan.2.status": "enabled", "switch.vlan.2.port.1.mode": "untagged",
			"switch.port.1.opmode": "switch",
		}
		curOld := brLanUCI("wan")
		curOld["network.uowvlan20"], curOld["network.uowvlan20.name"], curOld["network.uowvlan20.type"], curOld["network.uowvlan20.ports"] = "device", "br-uow20", "bridge", "wan.20"
		curOld["network.uow20"], curOld["network.uow20.device"], curOld["network.uow20.proto"] = "interface", "br-uow20", "none"
		curOld["network.uowvlan40"], curOld["network.uowvlan40.name"], curOld["network.uowvlan40.type"], curOld["network.uowvlan40.ports"] = "device", "br-uow40", "bridge", "'wan.40' 'lan'"
		curOld["network.uow40"], curOld["network.uow40.device"], curOld["network.uow40.proto"] = "interface", "br-uow40", "none"

		p := runPlanVLAN(cfg, curOld, "wan", ports)
		for k, want := range map[string][]string{
			"network.d.ports":         {"wan", "lan"},
			"network.uowbv20.ports":   {"wan:t", "lan:u*"},
			"network.uowvlan20.ports": {"br-lan.20"},
		} {
			if !slices.Equal(p.lists[k], want) {
				t.Fatalf("%s = %v, want %v", k, p.lists[k], want)
			}
		}
		if !p.dels["network.uow40"] || !p.dels["network.uowvlan40"] {
			t.Fatalf("expected old uow40/uowvlan40 deleted, got %v", p.dels)
		}
	})
}

func TestVLANSwitchPortDisabledAndReenable(t *testing.T) {
	ports := map[int]string{1: "lan"}
	// Real UniFi wire format when disabling a port without Port VLAN enabled.
	cfgOff := Cfg{
		"switch.status": "disabled", "switch.vlan.status": "disabled",
		"switch.port.1.status": "disabled", "switch.port.1.opmode": "switch",
	}
	p := runPlanVLAN(cfgOff, brLanUCI("wan lan"), "wan", ports)
	if !slices.Equal(p.lists["network.d.ports"], []string{"wan"}) || !p.hasNote("disabled") {
		t.Fatalf("disabled port: lists=%v notes=%v", p.lists, p.notes)
	}

	// status=disabled takes precedence over switch.vlan entries when Port VLAN is enabled.
	cfgWithVLAN := Cfg{
		"switch.status": "enabled", "switch.vlan.status": "enabled",
		"switch.vlan.4.id": "40", "switch.vlan.4.status": "enabled", "switch.vlan.4.port.1.mode": "untagged",
		"switch.port.1.status": "disabled", "switch.port.1.opmode": "switch",
	}
	if p := runPlanVLAN(cfgWithVLAN, brLanUCI("wan lan"), "wan", ports); p.vals["network.uowbv40"] != "" {
		t.Fatalf("disabled port must not create uowbv40, got %v", p.ops)
	}

	// Re-enabling restores lan to br-lan even with switch.status=disabled.
	cfgOn := Cfg{"switch.status": "disabled", "switch.vlan.status": "disabled", "switch.port.1.opmode": "switch"}
	if p := runPlanVLAN(cfgOn, brLanUCI("wan"), "wan", ports); !slices.Equal(p.lists["network.d.ports"], []string{"wan", "lan"}) {
		t.Fatalf("re-enabled br-lan ports = %v, want [wan lan]", p.lists["network.d.ports"])
	}
}

func TestVLANIDValidation(t *testing.T) {
	cfg := Cfg{
		"wireless.1.devname": "wifi0ap0", "wireless.2.devname": "wifi0ap1",
		"wireless.3.devname": "wifi0ap2", "wireless.4.devname": "wifi0ap3",
		"wireless.5.devname": "wifi0ap4",
		"bridge.1.devname":   "br0.0", "bridge.1.port.1.devname": "wifi0ap0",
		"bridge.2.devname": "br0.4095", "bridge.2.port.1.devname": "wifi0ap1",
		"bridge.3.devname": "br0.077", "bridge.3.port.1.devname": "wifi0ap2",
		"bridge.4.devname": "br0.+40", "bridge.4.port.1.devname": "wifi0ap3",
		"bridge.5.devname": "br0.4094", "bridge.5.port.1.devname": "wifi0ap4",
	}
	netOf, ids := vlanNetworks(cfg)
	if !slices.Equal(ids, []string{"4094"}) || netOf["wifi0ap4"] != "uow4094" || len(netOf) != 1 {
		t.Fatalf("vlanNetworks = (%v, %v), want only VLAN 4094", netOf, ids)
	}

	swCfg := Cfg{
		"switch.status": "enabled", "switch.vlan.status": "enabled",
		"switch.vlan.1.id": "0", "switch.vlan.1.status": "enabled", "switch.vlan.1.port.1.mode": "untagged",
		"switch.vlan.2.id": "4095", "switch.vlan.2.status": "enabled", "switch.vlan.2.port.1.mode": "tagged",
		"switch.vlan.3.id": "040", "switch.vlan.3.status": "enabled", "switch.vlan.3.port.1.mode": "tagged",
		"switch.vlan.4.id": "+40", "switch.vlan.4.status": "enabled", "switch.vlan.4.port.1.mode": "tagged",
		"switch.vlan.5.id": "abc", "switch.vlan.5.status": "enabled", "switch.vlan.5.port.1.mode": "tagged",
		"switch.vlan.6.id": "40", "switch.vlan.6.status": "enabled", "switch.vlan.6.port.1.mode": "tagged",
		"switch.vlan.7.id": "40", "switch.vlan.7.status": "enabled", "switch.vlan.7.port.1.mode": "untagged",
	}
	sp := switchPortPlan(swCfg, "wan", map[int]string{1: "lan"})
	if !slices.Equal(sp.extraVLANs, []string{"40"}) || !slices.Equal(sp.bvMembers["40"], []string{"lan:t"}) || len(sp.notes) != 1 {
		t.Fatalf("switchPortPlan = (%v, %v, %v), want single deduped VLAN 40 tagged", sp.extraVLANs, sp.bvMembers, sp.notes)
	}
}

func TestVLANSwitchGuards(t *testing.T) {
	t.Run("used elsewhere", func(t *testing.T) {
		cur := brLanUCI("wan")
		cur["network.dmz"], cur["network.dmz.device"] = "interface", "lan1"
		cur["network.br_iot"], cur["network.br_iot.ports"] = "device", "'lan2.99'"
		cur["network.v50"], cur["network.v50.type"], cur["network.v50.ifname"] = "device", "8021q", "lan3"
		cur["network.ext"], cur["network.ext.ifname"] = "interface", "lan4"
		cur["wireless.default_radio0"], cur["wireless.default_radio0.device"] = "wifi-iface", "lan5"
		ports := map[int]string{1: "lan1", 2: "lan2", 3: "lan3", 4: "lan4", 5: "lan5"}
		cfg := Cfg{
			"switch.status": "enabled", "switch.vlan.status": "enabled",
			"switch.vlan.1.id": "1", "switch.vlan.1.status": "enabled", "switch.vlan.1.port.1.mode": "exclude",
			"switch.vlan.4.id": "40", "switch.vlan.4.status": "enabled", "switch.vlan.4.port.1.mode": "untagged",
			"switch.port.1.opmode": "switch", "switch.port.2.opmode": "switch",
			"switch.port.3.opmode": "switch", "switch.port.4.opmode": "switch", "switch.port.5.opmode": "switch",
		}
		p := runPlanVLAN(cfg, cur, "wan", ports)
		if !slices.Equal(p.lists["network.d.ports"], []string{"wan", "lan5"}) {
			t.Fatalf("br-lan ports = %v, want [wan lan5]", p.lists["network.d.ports"])
		}
		if p.vals["network.uowbv40"] != "" {
			t.Fatalf("usedElsewhere port must not create uowbv40, got %v", p.ops)
		}
	})

	t.Run("uplink protected", func(t *testing.T) {
		cfg := Cfg{"switch.status": "enabled", "switch.port.1.opmode": "disabled"}
		if p := runPlanVLAN(cfg, brLanUCI("wan lan"), "wan", map[int]string{1: "wan"}); len(p.ops) != 0 {
			t.Fatalf("uplink port must never be removed, got %v", p.ops)
		}
	})

	t.Run("uplink outside br-lan", func(t *testing.T) {
		cfg := Cfg{
			"switch.status": "enabled", "switch.vlan.status": "enabled",
			"switch.vlan.1.id": "1", "switch.vlan.1.status": "enabled", "switch.vlan.1.port.1.mode": "exclude",
			"switch.vlan.4.id": "40", "switch.vlan.4.status": "enabled", "switch.vlan.4.port.1.mode": "untagged",
			"switch.port.1.opmode": "switch",
		}
		p := runPlanVLAN(cfg, brLanUCI("lan1"), "wan", map[int]string{1: "lan1"})
		if len(p.ops) != 0 || !p.hasNote("not in br-lan") {
			t.Fatalf("expected skip with note, got ops=%v notes=%v", p.ops, p.notes)
		}
	})

	t.Run("missing switchPorts keeps active bridge-vlan", func(t *testing.T) {
		cfg := Cfg{
			"wireless.1.devname": "wifi0ap1", "wireless.1.ssid": "home",
			"bridge.2.devname": "br0.20", "bridge.2.port.1.devname": "wifi0ap1", "bridge.2.port.2.devname": "eth0.20",
			"switch.status": "enabled", "switch.vlan.status": "enabled",
		}
		curBV := UCI{
			"network.d": "device", "network.d.name": "br-lan", "network.d.ports": "wan lan", "network.d.vlan_filtering": "1",
			"network.lan": "interface", "network.lan.device": "br-lan.1",
			"network.uowbv1": "bridge-vlan", "network.uowbv1.device": "br-lan", "network.uowbv1.vlan": "1", "network.uowbv1.ports": "'wan:u*'",
			"network.uowbv20": "bridge-vlan", "network.uowbv20.device": "br-lan", "network.uowbv20.vlan": "20", "network.uowbv20.ports": "'wan:t'",
			"network.uowvlan20": "device", "network.uowvlan20.name": "br-uow20", "network.uowvlan20.type": "bridge", "network.uowvlan20.ports": "'br-lan.20'",
			"network.uow20": "interface", "network.uow20.device": "br-uow20", "network.uow20.proto": "none",
		}
		p := runPlanVLAN(cfg, curBV, "wan", nil)
		if len(p.ops) != 0 || !p.hasNote("no downlink ports") {
			t.Fatalf("missing switchPorts tore down bridge-vlan: ops=%v notes=%v", p.ops, p.notes)
		}
	})

	t.Run("user bridge-vlan preserved on revert", func(t *testing.T) {
		cur := UCI{
			"network.d": "device", "network.d.name": "br-lan", "network.d.ports": "wan lan", "network.d.vlan_filtering": "1",
			"network.lan": "interface", "network.lan.device": "br-lan.1",
			"network.custom_bv": "bridge-vlan", "network.custom_bv.device": "br-lan", "network.custom_bv.vlan": "99",
			"network.uowbv1": "bridge-vlan", "network.uowbv1.device": "br-lan", "network.uowbv1.vlan": "1", "network.uowbv1.ports": "'wan:u*' 'lan:u*'",
		}
		p := runPlanVLAN(Cfg{"switch.status": "disabled"}, cur, "wan", map[int]string{1: "lan"})
		if !p.dels["network.uowbv1"] || p.dels["network.d.vlan_filtering"] || p.vals["network.lan.device"] != "" {
			t.Fatalf("user bridge-vlan revert: vals=%v dels=%v", p.vals, p.dels)
		}
	})

	t.Run("vlan1 modes and non-device br-lan", func(t *testing.T) {
		cur := UCI{
			"network.aaa": "interface", "network.aaa.name": "br-lan",
			"network.zzz": "device", "network.zzz.name": "br-lan", "network.zzz.ports": "wan lan",
			"network.lan": "interface", "network.lan.device": "br-lan",
		}
		cfgTag1 := Cfg{
			"switch.status": "enabled", "switch.vlan.status": "enabled",
			"switch.vlan.1.id": "1", "switch.vlan.1.status": "enabled", "switch.vlan.1.port.1.mode": "tagged",
		}
		p := runPlanVLAN(cfgTag1, cur, "wan", map[int]string{1: "lan"})
		if p.vals["network.zzz.vlan_filtering"] != "1" || !slices.Equal(p.lists["network.uowbv1.ports"], []string{"wan:u*", "lan:u*"}) || !p.hasNote("tagged vlan 1") {
			t.Fatalf("tagged VLAN 1: vals=%v lists=%v notes=%v", p.vals, p.lists, p.notes)
		}

		cfgNoV1 := Cfg{"switch.status": "enabled", "switch.vlan.status": "enabled", "switch.vlan.1.id": "1", "switch.vlan.1.status": "disabled"}
		if p := runPlanVLAN(cfgNoV1, cur, "wan", map[int]string{1: "lan"}); !slices.Equal(p.lists["network.uowbv1.ports"], []string{"wan:u*"}) {
			t.Fatalf("disabled VLAN 1 uowbv1.ports = %v, want [wan:u*]", p.lists["network.uowbv1.ports"])
		}
	})
}
