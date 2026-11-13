// SPDX-License-Identifier: GPL-2.0-or-later

package device

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Guest portal on an AP (UniFi Network 10.6, guest manager): after a portal
// login the controller does not push authorize-guest; it sends the guest's
// browser to http://<AP>:8882/redirect?ec=<cookie>. The cookie is the guest
// context as JSON + "\n", AES-128-CBC/PKCS5 with key = IV = hex(guest_token),
// base64url without padding. The AP reports guest_token in its inform; the
// controller copies it to the device record. See docs/internals.md.

// PortalPort is where UniFi APs take the redirect ("portal.redirector.port.wired").
const PortalPort = 8882

// NewGuestToken returns a random token in the controller's format (16 bytes,
// 32 upper-case hex). Secret: it decrypts the portal cookies for this AP.
func NewGuestToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}

// DecryptEC opens an encrypted portal cookie.
func DecryptEC(token, ec string) (map[string]any, error) {
	key, err := hex.DecodeString(token)
	if err != nil || len(key) != 16 {
		return nil, errors.New("bad guest token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(ec, "="))
	if err != nil {
		return nil, fmt.Errorf("ec: %w", err)
	}
	if len(raw) == 0 || len(raw)%aes.BlockSize != 0 {
		return nil, errors.New("ec: not whole AES blocks")
	}
	blk, _ := aes.NewCipher(key)
	out := make([]byte, len(raw))
	cipher.NewCBCDecrypter(blk, key).CryptBlocks(out, raw)
	n := int(out[len(out)-1])
	if n < 1 || n > aes.BlockSize || !bytes.Equal(out[len(out)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, errors.New("ec: bad padding (wrong token?)")
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out[:len(out)-n]), &m); err != nil {
		return nil, fmt.Errorf("ec: %w", err)
	}
	return m, nil
}

// EncryptEC is the controller's side (for tests).
func EncryptEC(token string, m map[string]any) (string, error) {
	key, err := hex.DecodeString(token)
	if err != nil || len(key) != 16 {
		return "", errors.New("bad guest token")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	n := aes.BlockSize - len(b)%aes.BlockSize
	b = append(b, bytes.Repeat([]byte{byte(n)}, n)...)
	blk, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(blk, key).CryptBlocks(b, b)
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PortalGuest is what the AP needs from a cookie.
type PortalGuest struct {
	MAC    string
	Expire int64  // unix seconds
	URL    string // where the guest wanted to go
}

// ParsePortalGuest validates a decrypted cookie: it must name this AP and a
// client MAC, and carry an expiry in the future.
func ParsePortalGuest(m map[string]any, apMAC string, now int64) (PortalGuest, error) {
	s := func(k string) string { v, _ := m[k].(string); return v }
	g := PortalGuest{URL: s("url")}
	macs := ParseMACs(s("mac"))
	if len(macs) != 1 {
		return g, fmt.Errorf("cookie mac %q", s("mac"))
	}
	g.MAC = macs[0]
	if ap := ParseMACs(s("ap")); len(ap) != 1 || !strings.EqualFold(ap[0], apMAC) {
		return g, fmt.Errorf("cookie is for AP %q, not %s", s("ap"), apMAC)
	}
	switch v := m["expire"].(type) {
	case float64:
		g.Expire = int64(v)
	case string:
		fmt.Sscan(v, &g.Expire)
	}
	if g.Expire > 1e12 { // milliseconds
		g.Expire /= 1000
	}
	if g.Expire <= now {
		return g, fmt.Errorf("cookie expired or without expiry (%v)", m["expire"])
	}
	return g, nil
}
