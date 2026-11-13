// SPDX-License-Identifier: GPL-2.0-or-later

package device

import (
	"bytes"
	"strings"
	"testing"
)

func TestSyslogSeal(t *testing.T) {
	key := strings.Repeat("0123456789abcdef", 4)
	id, err := NewHashID()
	if err != nil || len(id) != 16 {
		t.Fatalf("hash_id %q %v", id, err)
	}
	msg := []byte("<30>Nov 13 00:40:00 OnHub U7PG2,085566770001,6.8.2.15592: hostapd: phy1-ap0: AP-STA-CONNECTED")
	d, err := SealSyslog(key, id, msg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d[:9], []byte{0xAB, 0xCD, 0xDC, 0xBA, 1, 0, 0, 0, 1}) {
		t.Fatalf("header % x", d[:9])
	}
	gotID, got, err := OpenSyslog(key, d)
	if err != nil || gotID != id || !bytes.Equal(got, msg) {
		t.Fatalf("open: %q %q %v", gotID, got, err)
	}
	d[20] ^= 1 // the nonce is in the AAD: any header change must fail
	if _, _, err := OpenSyslog(key, d); err == nil {
		t.Error("tampered header accepted")
	}
	big, _ := SealSyslog(key, id, bytes.Repeat([]byte("x"), 9000))
	if len(big) > MaxSyslogDatagram {
		t.Errorf("datagram %d > %d", len(big), MaxSyslogDatagram)
	}
}
