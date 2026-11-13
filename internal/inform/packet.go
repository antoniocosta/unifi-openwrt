// SPDX-License-Identifier: GPL-2.0-or-later

// Package inform encodes and decodes UniFi "inform" packets (the TNBU frame
// a UniFi device POSTs to http://<controller>:8080/inform, and the frame the
// controller answers with).
//
// Wire format, as documented by open-unifi docs/PROTOCOL.md §1 and unifi-emu
// docs/PROTOCOL.md (see also docs/internals.md here):
//
//	off size field
//	0   4    magic "TNBU"
//	4   4    packet version (BE; controller does not check it)
//	8   6    device MAC
//	14  2    flags (BE): 0x01 encrypted, 0x02 zlib, 0x04 snappy, 0x08 AES-GCM
//	16  16   IV (also the GCM nonce, 16 bytes long)
//	32  4    data version (BE), must be 1
//	36  4    payload length (BE); for GCM it includes the 16-byte tag
//	40  n    payload
//
// CBC: AES-128-CBC, PKCS#7. GCM: 16-byte nonce, AAD = the full 40-byte
// header as sent (payload length already filled in), tag appended.
// The plaintext is zlib-compressed JSON.
package inform

import (
	"bytes"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
)

const (
	Magic       uint32 = 0x544E4255 // "TNBU"
	HeaderLen          = 40
	DataVersion uint32 = 1
	// PacketVersion is what a device sends. open-unifi (from the controller
	// bytecode) says devices send 0; unifi-emu sends 1. The controller does
	// not dispatch on it. We follow the bytecode reading.
	PacketVersion uint32 = 0
	MaxBodySize          = 10 << 20 // controller rejects bodies > 10 MB

	FlagEncrypted uint16 = 0x01
	FlagZlib      uint16 = 0x02
	FlagSnappy    uint16 = 0x04
	FlagGCM       uint16 = 0x08

	// DefaultKeyHex is MD5("ubnt"): the key every unadopted device uses.
	DefaultKeyHex = "ba86f2bbe107c7c57eb5f2690775c712"

	gcmTagLen = 16
)

// Cipher picks the encryption mode of an outgoing packet.
type Cipher int

const (
	CBC Cipher = iota // before adoption (flags 0x0003)
	GCM               // after mgmt_cfg use_aes_gcm=true (flags 0x000b)
)

// Header is the 40-byte frame header.
type Header struct {
	Version     uint32
	MAC         net.HardwareAddr
	Flags       uint16
	IV          [16]byte
	DataVersion uint32
	PayloadLen  uint32
}

func (h *Header) marshal() ([]byte, error) {
	if len(h.MAC) != 6 {
		return nil, fmt.Errorf("inform: MAC must be 6 bytes, got %d", len(h.MAC))
	}
	b := make([]byte, HeaderLen)
	binary.BigEndian.PutUint32(b[0:4], Magic)
	binary.BigEndian.PutUint32(b[4:8], h.Version)
	copy(b[8:14], h.MAC)
	binary.BigEndian.PutUint16(b[14:16], h.Flags)
	copy(b[16:32], h.IV[:])
	binary.BigEndian.PutUint32(b[32:36], h.DataVersion)
	binary.BigEndian.PutUint32(b[36:40], h.PayloadLen)
	return b, nil
}

// ParseHeader reads and validates the frame header; it returns the header
// and the payload slice (exactly PayloadLen bytes).
func ParseHeader(body []byte) (*Header, []byte, error) {
	if len(body) > MaxBodySize {
		return nil, nil, errors.New("inform: body too large")
	}
	if len(body) < HeaderLen {
		return nil, nil, fmt.Errorf("inform: body too short (%d bytes)", len(body))
	}
	if m := binary.BigEndian.Uint32(body[0:4]); m != Magic {
		return nil, nil, fmt.Errorf("inform: bad magic %#08x", m)
	}
	h := &Header{
		Version:     binary.BigEndian.Uint32(body[4:8]),
		MAC:         net.HardwareAddr(append([]byte(nil), body[8:14]...)),
		Flags:       binary.BigEndian.Uint16(body[14:16]),
		DataVersion: binary.BigEndian.Uint32(body[32:36]),
		PayloadLen:  binary.BigEndian.Uint32(body[36:40]),
	}
	copy(h.IV[:], body[16:32])
	if h.DataVersion != DataVersion {
		return nil, nil, fmt.Errorf("inform: data version %d not supported", h.DataVersion)
	}
	if uint64(h.PayloadLen) > uint64(len(body)-HeaderLen) {
		return nil, nil, fmt.Errorf("inform: payload length %d exceeds body (%d)", h.PayloadLen, len(body)-HeaderLen)
	}
	return h, body[HeaderLen : HeaderLen+int(h.PayloadLen)], nil
}

// ParseKey turns a 32-hex-char authkey into the 16-byte AES key (no KDF).
func ParseKey(hexKey string) ([]byte, error) {
	k, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("inform: key is not hex: %w", err)
	}
	if len(k) != 16 {
		return nil, fmt.Errorf("inform: key must be 16 bytes, got %d", len(k))
	}
	return k, nil
}

// Encode builds a complete packet: zlib(JSON) encrypted with key in mode c.
// iv may be nil (a random one is generated); tests pass a fixed IV.
func Encode(mac net.HardwareAddr, keyHex string, c Cipher, plain []byte, iv []byte) ([]byte, error) {
	key, err := ParseKey(keyHex)
	if err != nil {
		return nil, err
	}
	h := &Header{Version: PacketVersion, MAC: mac, DataVersion: DataVersion, Flags: FlagEncrypted | FlagZlib}
	if iv == nil {
		if _, err := rand.Read(h.IV[:]); err != nil {
			return nil, err
		}
	} else if len(iv) != 16 {
		return nil, fmt.Errorf("inform: IV must be 16 bytes, got %d", len(iv))
	} else {
		copy(h.IV[:], iv)
	}

	var zbuf bytes.Buffer
	zw := zlib.NewWriter(&zbuf)
	if _, err := zw.Write(plain); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	comp := zbuf.Bytes()

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	var payload []byte
	switch c {
	case CBC:
		padded := pkcs7Pad(comp, aes.BlockSize)
		payload = make([]byte, len(padded))
		cipher.NewCBCEncrypter(block, h.IV[:]).CryptBlocks(payload, padded)
		h.PayloadLen = uint32(len(payload))
	case GCM:
		h.Flags |= FlagGCM
		aead, err := cipher.NewGCMWithNonceSize(block, 16)
		if err != nil {
			return nil, err
		}
		h.PayloadLen = uint32(len(comp) + gcmTagLen)
		aad, err := h.marshal() // AAD is the header exactly as sent
		if err != nil {
			return nil, err
		}
		payload = aead.Seal(nil, h.IV[:], comp, aad)
	default:
		return nil, fmt.Errorf("inform: unknown cipher %d", c)
	}
	hdr, err := h.marshal()
	if err != nil {
		return nil, err
	}
	return append(hdr, payload...), nil
}

// Decode parses a packet and returns its header and the decrypted,
// decompressed JSON. A controller's reply is decoded with the same function.
func Decode(body []byte, keyHex string) (*Header, []byte, error) {
	h, payload, err := ParseHeader(body)
	if err != nil {
		return nil, nil, err
	}
	data := payload
	switch {
	case h.Flags&FlagGCM != 0:
		key, err := ParseKey(keyHex)
		if err != nil {
			return nil, nil, err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, nil, err
		}
		aead, err := cipher.NewGCMWithNonceSize(block, 16)
		if err != nil {
			return nil, nil, err
		}
		aad := body[:HeaderLen]
		data, err = aead.Open(nil, h.IV[:], payload, aad)
		if err != nil {
			return nil, nil, fmt.Errorf("inform: GCM open (wrong key or tampered): %w", err)
		}
	case h.Flags&FlagEncrypted != 0:
		key, err := ParseKey(keyHex)
		if err != nil {
			return nil, nil, err
		}
		if len(payload) == 0 || len(payload)%aes.BlockSize != 0 {
			return nil, nil, fmt.Errorf("inform: CBC payload length %d not a block multiple", len(payload))
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, nil, err
		}
		dec := make([]byte, len(payload))
		cipher.NewCBCDecrypter(block, h.IV[:]).CryptBlocks(dec, payload)
		data, err = pkcs7Unpad(dec, aes.BlockSize)
		if err != nil {
			return nil, nil, err
		}
	}
	switch {
	case h.Flags&FlagSnappy != 0:
		return nil, nil, errors.New("inform: snappy payloads not supported (U7PG2 6.8.2 never sends them)")
	case h.Flags&FlagZlib != 0:
		zr, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, nil, fmt.Errorf("inform: zlib (wrong key?): %w", err)
		}
		defer zr.Close()
		data, err = io.ReadAll(io.LimitReader(zr, MaxBodySize*4))
		if err != nil {
			return nil, nil, fmt.Errorf("inform: zlib: %w", err)
		}
	}
	return h, data, nil
}

func pkcs7Pad(b []byte, bs int) []byte {
	n := bs - len(b)%bs
	return append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte, bs int) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("inform: empty CBC plaintext")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > bs || n > len(b) {
		return nil, errors.New("inform: bad PKCS#7 padding (wrong key?)")
	}
	for _, x := range b[len(b)-n:] {
		if int(x) != n {
			return nil, errors.New("inform: bad PKCS#7 padding (wrong key?)")
		}
	}
	return b[:len(b)-n], nil
}
