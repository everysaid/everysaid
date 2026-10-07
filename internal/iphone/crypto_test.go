package iphone

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"testing"
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// RFC 3394, section 4: the test vectors.
var rfc3394 = []struct{ kek, key, wrapped string }{
	{"000102030405060708090A0B0C0D0E0F", "00112233445566778899AABBCCDDEEFF",
		"1FA68B0A8112B447AEF34BD8FB5A7B829D3E862371D2CFE5"},
	{"000102030405060708090A0B0C0D0E0F1011121314151617", "00112233445566778899AABBCCDDEEFF",
		"96778B25AE6CA435F92B5B97C050AED2468AB8A17AD84E5D"},
	{"000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F", "00112233445566778899AABBCCDDEEFF",
		"64E8C3F9CE0F5BA263E9777905818A2A93C8191E7D6E8AE7"},
	{"000102030405060708090A0B0C0D0E0F1011121314151617", "00112233445566778899AABBCCDDEEFF0001020304050607",
		"031D33264E15D33268F24EC260743EDCE1C6C7DDEE725A936BA814915C6762D2"},
	{"000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F", "00112233445566778899AABBCCDDEEFF0001020304050607",
		"A8F9BC1612C68B3FF6E6F4FBE30E71E4769C8B80A32CB8958CD5D17D6B254DA1"},
	{"000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F",
		"00112233445566778899AABBCCDDEEFF000102030405060708090A0B0C0D0E0F",
		"28C9F404C4B810F4CBCCB35CFB87F8263F5786E2D80ED326CBC7F0E71A99F43BFB988B9B7A02DD21"},
}

func TestAESUnwrapRFC3394(t *testing.T) {
	for i, v := range rfc3394 {
		got, err := aesUnwrap(unhex(v.kek), unhex(v.wrapped))
		if err != nil || !bytes.Equal(got, unhex(v.key)) {
			t.Errorf("vector %d: unwrap %x, %v", i, got, err)
		}
		wrapped, err := aesWrap(unhex(v.kek), unhex(v.key))
		if err != nil || !bytes.Equal(wrapped, unhex(v.wrapped)) {
			t.Errorf("vector %d: wrap %x, %v", i, wrapped, err)
		}
		bad := unhex(v.kek)
		bad[0] ^= 1
		if _, err := aesUnwrap(bad, unhex(v.wrapped)); err != errUnwrap {
			t.Errorf("vector %d: a wrong key unwrapped (%v)", i, err)
		}
		tampered := unhex(v.wrapped)
		tampered[len(tampered)-1] ^= 1
		if _, err := aesUnwrap(unhex(v.kek), tampered); err != errUnwrap {
			t.Errorf("vector %d: tampered data unwrapped (%v)", i, err)
		}
	}
	if _, err := aesUnwrap(unhex(rfc3394[0].kek), make([]byte, 20)); err == nil {
		t.Error("a length not a multiple of 8 unwrapped")
	}
}

func TestUnpad(t *testing.T) {
	cases := []struct {
		in   []byte
		want []byte
		ok   bool
	}{
		{append([]byte("abc"), bytes.Repeat([]byte{13}, 13)...), []byte("abc"), true},
		{bytes.Repeat([]byte{16}, 16), []byte{}, true},
		{append([]byte("abc"), 0), nil, false},                // 0 is no padding
		{append(bytes.Repeat([]byte{1}, 15), 17), nil, false}, // more than a block
		{append([]byte("abcdefghijklmn"), 1, 2), nil, false},  // bytes not all the same
		{append([]byte("abcdefghijklmn"), 2, 2), []byte("abcdefghijklmn"), true},
		{[]byte{}, nil, false},
	}
	for i, c := range cases {
		got, err := unpad(c.in)
		if (err == nil) != c.ok || (c.ok && !bytes.Equal(got, c.want)) {
			t.Errorf("case %d: %x, %v", i, got, err)
		}
	}
}

// encrypt is AES-CBC with a zero IV and RFC 1423 padding, as a backup's files are.
func encrypt(key, plain []byte) []byte {
	n := aes.BlockSize - len(plain)%aes.BlockSize
	data := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(n)}, n)...)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, make([]byte, aes.BlockSize)).CryptBlocks(data, data)
	return data
}

func TestDecryptStream(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	for _, size := range []int{0, 1, 15, 16, 17, chunkSize - 1, chunkSize, chunkSize + 5, 3*chunkSize + 100} {
		plain := make([]byte, size)
		for i := range plain {
			plain[i] = byte(i * 31)
		}
		enc := encrypt(key, plain)
		var out bytes.Buffer
		n, err := decryptStream(bytes.NewReader(enc), int64(len(enc)), key, &out)
		if err != nil || n != int64(size) || !bytes.Equal(out.Bytes(), plain) {
			t.Errorf("size %d: %d, %v", size, n, err)
		}
	}
	if _, err := decryptStream(bytes.NewReader(make([]byte, 17)), 17, key, &bytes.Buffer{}); err != errBlocks {
		t.Errorf("17 bytes: %v", err)
	}
	if n, err := decryptStream(bytes.NewReader(nil), 0, key, &bytes.Buffer{}); err != nil || n != 0 {
		t.Errorf("an empty file: %d, %v", n, err)
	}
	wrong := encrypt(bytes.Repeat([]byte{8}, 32), []byte("hello"))
	if _, err := decryptStream(bytes.NewReader(wrong), int64(len(wrong)), key, &bytes.Buffer{}); err == nil {
		// a wrong key gives a padding that checks only by chance (about 1 in 256)
		t.Log("a wrong key gave a valid padding")
	}
}
