// SPDX-License-Identifier: GPL-2.0-or-later

package inform

import (
	"encoding/json"
	"testing"
)

func testDevice() Device {
	return Device{
		MAC:       testMAC,
		IP:        "192.168.1.50",
		Netmask:   "255.255.255.0",
		Gateway:   "192.168.1.1",
		Hostname:  "unifi-openwrt-test",
		InformURL: "http://192.168.1.1:8080/inform",
		Uptime:    42,
		Time:      1794500000,
	}
}

// TestU7PG2PendingInformOffline is the M0 "done when": build a U7PG2
// pending inform, encode it with the default key, decode it, check fields.
func TestU7PG2PendingInformOffline(t *testing.T) {
	js, err := PendingInform(testDevice())
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := Encode(testMAC, DefaultKeyHex, CBC, js, nil)
	if err != nil {
		t.Fatal(err)
	}
	h, out, err := Decode(pkt, DefaultKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	if h.Flags != 0x0003 {
		t.Errorf("pending inform flags %#04x, want 0x0003 (as the real U7PG2)", h.Flags)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["mac"] != h.MAC.String() {
		t.Errorf("payload mac %v != header mac %s", m["mac"], h.MAC)
	}
	for _, k := range requiredPendingFields {
		if _, ok := m[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
	want := map[string]any{"model": "U7PG2", "type": "uap", "version": "6.8.2.15592",
		"state": 1.0, "default": true, "fw_caps": 3.0, "inform_url": "http://192.168.1.1:8080/inform"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	// udapi_caps without udapi_version makes the controller drop all caps.
	if _, caps := m["udapi_caps"]; caps {
		if _, ver := m["udapi_version"]; !ver {
			t.Error("udapi_caps without udapi_version")
		}
	}
	radios, _ := m["radio_table"].([]any)
	if len(radios) != 2 {
		t.Fatalf("radio_table has %d radios, want 2", len(radios))
	}
	t.Logf("pending inform: %d bytes JSON, %d bytes on the wire", len(js), len(pkt))
}

func TestU7PG2RejectsHostnameInformURL(t *testing.T) {
	d := testDevice()
	d.InformURL = "http://unifi:8080/inform"
	if _, err := PendingInform(d); err == nil {
		t.Fatal("hostname inform_url accepted (controller rejects it: invalid inform_ip)")
	}
}

// requiredPendingFields: docs/internals.md (every inform, pending or adopted).
var requiredPendingFields = []string{
	"mac", "serial", "model", "model_display", "type", "version", "ip", "hostname",
	"inform_url", "uptime", "time", "cfgversion", "x_authkey", "default",
	"_default_key", "state", "fw_caps", "isolated", "locating", "selfrun_beacon",
	"radio_table",
}
