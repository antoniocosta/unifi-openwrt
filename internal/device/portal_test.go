// SPDX-License-Identifier: GPL-2.0-or-later

package device

import (
	"strings"
	"testing"
)

func TestPortalCookie(t *testing.T) {
	tok, err := NewGuestToken()
	if err != nil || len(tok) != 32 || strings.ToUpper(tok) != tok {
		t.Fatalf("token %q %v", tok, err)
	}
	now := int64(1794500000)
	ec, err := EncryptEC(tok, map[string]any{"mac": "02:00:00:00:00:a1", "ap": "08:55:66:77:00:01",
		"url": "http://example.com/", "ssid": "GuestWiFi", "expire": now + 3600, "kicks": 0})
	if err != nil {
		t.Fatal(err)
	}
	m, err := DecryptEC(tok, ec)
	if err != nil {
		t.Fatal(err)
	}
	g, err := ParsePortalGuest(m, "08:55:66:77:00:01", now)
	if err != nil || g.MAC != "02:00:00:00:00:a1" || g.Expire != now+3600 || g.URL != "http://example.com/" {
		t.Fatalf("guest %+v %v", g, err)
	}
	if _, err := ParsePortalGuest(m, "40:11:22:33:44:01", now); err == nil {
		t.Error("a cookie for another AP must be refused")
	}
	if _, err := ParsePortalGuest(m, "08:55:66:77:00:01", now+7200); err == nil {
		t.Error("an expired cookie must be refused")
	}
	other, _ := NewGuestToken()
	if _, err := DecryptEC(other, ec); err == nil {
		t.Error("a cookie under another token must not open")
	}
}
