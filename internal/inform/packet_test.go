// SPDX-License-Identifier: GPL-2.0-or-later

package inform

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net"
	"testing"
)

// A locally administered MAC, as M1 will use: never the OnHub's.
var testMAC = net.HardwareAddr{0x02, 0x00, 0x5e, 0x10, 0x00, 0x01}

func TestDefaultKeyIsMD5ubnt(t *testing.T) {
	sum := md5.Sum([]byte("ubnt"))
	if got := hex.EncodeToString(sum[:]); got != DefaultKeyHex {
		t.Fatalf("MD5(ubnt) = %s, DefaultKeyHex = %s", got, DefaultKeyHex)
	}
}

func TestRoundTrip(t *testing.T) {
	plain := []byte(`{"_type":"info","mac":"02:00:5e:10:00:01","model":"U7PG2"}`)
	adopted := "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name      string
		c         Cipher
		key       string
		wantFlags uint16
	}{
		{"pending CBC default key", CBC, DefaultKeyHex, 0x0003},
		{"adopted GCM own key", GCM, adopted, 0x000b},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkt, err := Encode(testMAC, tc.key, tc.c, plain, nil)
			if err != nil {
				t.Fatal(err)
			}
			h, got, err := Decode(pkt, tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("round trip: got %q", got)
			}
			if h.Flags != tc.wantFlags {
				t.Errorf("flags = %#04x, want %#04x", h.Flags, tc.wantFlags)
			}
			if !bytes.Equal(h.MAC, testMAC) {
				t.Errorf("MAC = %s", h.MAC)
			}
			if int(h.PayloadLen) != len(pkt)-HeaderLen {
				t.Errorf("payload len %d, body %d", h.PayloadLen, len(pkt)-HeaderLen)
			}
			// Wrong key must fail, not return garbage.
			if _, _, err := Decode(pkt, adoptedOr(tc.key)); err == nil {
				t.Error("decode with the wrong key succeeded")
			}
		})
	}
}

func adoptedOr(k string) string {
	if k == DefaultKeyHex {
		return "0123456789abcdef0123456789abcdef"
	}
	return DefaultKeyHex
}

// TestHeaderLayout checks every byte offset of the 40-byte header.
func TestHeaderLayout(t *testing.T) {
	iv := bytes.Repeat([]byte{0xAB}, 16)
	pkt, err := Encode(testMAC, DefaultKeyHex, CBC, []byte(`{}`), iv)
	if err != nil {
		t.Fatal(err)
	}
	if string(pkt[0:4]) != "TNBU" {
		t.Errorf("magic %q", pkt[0:4])
	}
	if v := binary.BigEndian.Uint32(pkt[4:8]); v != PacketVersion {
		t.Errorf("packet version %d", v)
	}
	if !bytes.Equal(pkt[8:14], testMAC) {
		t.Errorf("mac % x", pkt[8:14])
	}
	if f := binary.BigEndian.Uint16(pkt[14:16]); f != 0x0003 {
		t.Errorf("flags %#04x", f)
	}
	if !bytes.Equal(pkt[16:32], iv) {
		t.Errorf("iv % x", pkt[16:32])
	}
	if dv := binary.BigEndian.Uint32(pkt[32:36]); dv != 1 {
		t.Errorf("data version %d", dv)
	}
	if n := binary.BigEndian.Uint32(pkt[36:40]); int(n) != len(pkt)-40 || n%16 != 0 {
		t.Errorf("payload len %d (body %d)", n, len(pkt)-40)
	}
}

// TestGCMHeaderIsAuthenticated: flipping any header byte must break GCM.
func TestGCMHeaderIsAuthenticated(t *testing.T) {
	pkt, err := Encode(testMAC, DefaultKeyHex, GCM, []byte(`{"a":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, off := range []int{4, 8, 13, 16, 31} { // version, MAC, IV
		bad := append([]byte(nil), pkt...)
		bad[off] ^= 0x01
		if _, _, err := Decode(bad, DefaultKeyHex); err == nil {
			t.Errorf("tampered header byte %d accepted", off)
		}
	}
}

func TestDeterministicWithFixedIV(t *testing.T) {
	iv := make([]byte, 16)
	a, _ := Encode(testMAC, DefaultKeyHex, CBC, []byte(`{"x":1}`), iv)
	b, _ := Encode(testMAC, DefaultKeyHex, CBC, []byte(`{"x":1}`), iv)
	if !bytes.Equal(a, b) {
		t.Fatal("same input and IV gave different packets")
	}
	c, _ := Encode(testMAC, DefaultKeyHex, CBC, []byte(`{"x":1}`), nil)
	if bytes.Equal(a[16:32], c[16:32]) {
		t.Fatal("random IV was all zero")
	}
}

func TestParseHeaderRejects(t *testing.T) {
	good, _ := Encode(testMAC, DefaultKeyHex, CBC, []byte(`{}`), nil)
	cases := map[string][]byte{
		"short":    good[:39],
		"magic":    append([]byte("XNBU"), good[4:]...),
		"data ver": func() []byte { b := append([]byte(nil), good...); b[35] = 2; return b }(),
		"length":   func() []byte { b := append([]byte(nil), good...); b[39]++; return b }(),
	}
	for name, b := range cases {
		if _, _, err := ParseHeader(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestControllerReply decodes replies built the way the controller builds
// them (open-unifi codec.Respond): CBC request -> flags 0x0001, no zlib;
// GCM request -> flags 0x0009. Our decoder must accept both.
func TestControllerReply(t *testing.T) {
	noop := []byte(`{"_type":"noop","interval":10,"server_time_in_utc":"1700000000000"}`)
	for _, gcm := range []bool{false, true} {
		reply := controllerStyleReply(t, gcm, noop)
		_, got, err := Decode(reply, DefaultKeyHex)
		if err != nil {
			t.Fatalf("gcm=%v: %v", gcm, err)
		}
		var m map[string]any
		if err := json.Unmarshal(got, &m); err != nil || m["_type"] != "noop" {
			t.Fatalf("gcm=%v: got %q (%v)", gcm, got, err)
		}
	}
}

// controllerStyleReply mimics the controller: uncompressed JSON, flags
// 0x0001 (CBC) or 0x0009 (GCM|encrypted).
func controllerStyleReply(t *testing.T, gcm bool, plain []byte) []byte {
	t.Helper()
	key, _ := ParseKey(DefaultKeyHex)
	h := &Header{Version: PacketVersion, MAC: testMAC, DataVersion: 1, Flags: FlagEncrypted}
	copy(h.IV[:], bytes.Repeat([]byte{7}, 16))
	var payload []byte
	if gcm {
		h.Flags |= FlagGCM
		h.PayloadLen = uint32(len(plain) + gcmTagLen)
		aad, _ := h.marshal()
		payload = sealGCM(t, key, h.IV[:], plain, aad)
	} else {
		payload = encryptCBC(t, key, h.IV[:], plain)
		h.PayloadLen = uint32(len(payload))
	}
	hdr, _ := h.marshal()
	return append(hdr, payload...)
}
