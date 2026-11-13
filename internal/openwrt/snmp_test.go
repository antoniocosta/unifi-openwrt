// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"strings"
	"testing"
)

func TestSNMPConfig(t *testing.T) {
	if c, err := SNMPConfig(map[string]string{}); c != "" || err != nil {
		t.Fatalf("off: %q %v", c, err)
	}
	c, err := SNMPConfig(map[string]string{"snmp.status": "enabled", "snmp.version": "all",
		"snmp.community": "s3cret", "snmp.location": "hall\nway", "snmp.contact": "me",
		"snmp.user.1.name": "mon", "snmp.user.1.password": "longenough"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"rocommunity s3cret default", "sysLocation hall way", "sysContact me",
		`createUser mon SHA "longenough" AES "longenough"`, "rouser mon priv"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	c, _ = SNMPConfig(map[string]string{"snmp.status": "enabled", "snmp.version": "3"})
	if c != "" {
		t.Errorf("v3 without a user must be off:\n%s", c)
	}
	if _, err := SNMPConfig(map[string]string{"snmp.status": "enabled", "snmp.version": "1_2c", "snmp.community": `bad"q`}); err == nil {
		t.Error("a quote in the community must be refused")
	}
}
