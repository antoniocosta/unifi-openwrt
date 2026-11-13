// SPDX-License-Identifier: GPL-2.0-or-later

package device

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

// UniFi encrypted remote syslog (UniFi Network 10.6.109, RemoteLoggerServer on
// UDP 5514; docs/internals.md). One datagram per message:
//
//	0  4  magic AB CD DC BA
//	4  1  0x01
//	5  4  00 00 00 01
//	9  8  device hash_id (the 16 hex chars the AP reports in its inform)
//	17 12 GCM nonce (random)
//	29 n  AES-256-GCM(key = hex(syslog.remote.key), AAD = bytes 0..28) || tag
//
// The plaintext is a syslog line; the controller writes it to
// logs/remote/<ip>_<mac>.log.

const syslogHeader = 29

// MaxSyslogDatagram is the controller's receive buffer.
const MaxSyslogDatagram = 4096

// NewHashID returns a random device hash_id in the controller's format
// (16 hex, not all zero).
func NewHashID() (string, error) {
	b := make([]byte, 8)
	for {
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		if s := hex.EncodeToString(b); s != "0000000000000000" {
			return s, nil
		}
	}
}

// SealSyslog builds one encrypted syslog datagram.
func SealSyslog(keyHex, hashID string, msg []byte) ([]byte, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, errors.New("syslog key: want 64 hex characters")
	}
	id, err := hex.DecodeString(strings.ToLower(hashID))
	if err != nil || len(id) != 8 {
		return nil, errors.New("hash_id: want 16 hex characters")
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	if max := MaxSyslogDatagram - syslogHeader - gcm.Overhead(); len(msg) > max {
		msg = msg[:max]
	}
	hdr := make([]byte, syslogHeader, syslogHeader+len(msg)+gcm.Overhead())
	copy(hdr, []byte{0xAB, 0xCD, 0xDC, 0xBA, 0x01, 0, 0, 0, 1})
	copy(hdr[9:17], id)
	if _, err := rand.Read(hdr[17:29]); err != nil {
		return nil, err
	}
	return gcm.Seal(hdr, hdr[17:29], msg, hdr[:syslogHeader]), nil
}

// OpenSyslog is the controller's side (for tests).
func OpenSyslog(keyHex string, d []byte) (hashID string, msg []byte, err error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 || len(d) < syslogHeader {
		return "", nil, errors.New("bad key or datagram")
	}
	if string(d[:9]) != string([]byte{0xAB, 0xCD, 0xDC, 0xBA, 0x01, 0, 0, 0, 1}) {
		return "", nil, errors.New("not an encrypted syslog datagram")
	}
	blk, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(blk)
	msg, err = gcm.Open(nil, d[17:29], d[syslogHeader:], d[:syslogHeader])
	return hex.EncodeToString(d[9:17]), msg, err
}
