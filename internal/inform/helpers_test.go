// SPDX-License-Identifier: GPL-2.0-or-later

package inform

import (
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func sealGCM(t *testing.T, key, iv, plain, aad []byte) []byte {
	t.Helper()
	b, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	g, err := cipher.NewGCMWithNonceSize(b, 16)
	if err != nil {
		t.Fatal(err)
	}
	return g.Seal(nil, iv, plain, aad)
}

func encryptCBC(t *testing.T, key, iv, plain []byte) []byte {
	t.Helper()
	b, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	p := pkcs7Pad(plain, aes.BlockSize)
	out := make([]byte, len(p))
	cipher.NewCBCEncrypter(b, iv).CryptBlocks(out, p)
	return out
}
