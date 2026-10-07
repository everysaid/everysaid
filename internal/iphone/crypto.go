// Ports the crypto of iphone_backup_decrypt/utils.py (the library iphone-sync.py uses): RFC 3394
// key unwrap, AES-CBC with a zero IV and its padding, a file decrypted in chunks.
package iphone

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const chunkSize = 1 << 20 // 1 MB, a multiple of the block

var (
	errUnwrap  = errors.New("AES key unwrap: integrity check failed")
	errPadding = errors.New("Invalid CBC padding on decrypted data!")
	errBlocks  = errors.New("Data for AES decryption length not a multiple of 16!")
)

var defaultIV = []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}

// aesUnwrap unwraps a key wrapped with the RFC 3394 key wrap algorithm (the default IV checked).
func aesUnwrap(kek, wrapped []byte) ([]byte, error) {
	if len(wrapped)%8 != 0 || len(wrapped) < 24 {
		return nil, errors.New("AES key unwrap: wrong length")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(wrapped)/8 - 1
	a := make([]byte, 8)
	copy(a, wrapped[:8])
	r := make([]byte, n*8)
	copy(r, wrapped[8:])
	buf := make([]byte, 16)
	for j := 5; j >= 0; j-- {
		for i := n; i >= 1; i-- {
			t := uint64(n*j + i)
			binary.BigEndian.PutUint64(buf[:8], binary.BigEndian.Uint64(a)^t)
			copy(buf[8:], r[(i-1)*8:i*8])
			block.Decrypt(buf, buf)
			copy(a, buf[:8])
			copy(r[(i-1)*8:i*8], buf[8:])
		}
	}
	if subtle.ConstantTimeCompare(a, defaultIV) != 1 {
		return nil, errUnwrap
	}
	return r, nil
}

// aesWrap is RFC 3394's wrap, for the tests' backups.
func aesWrap(kek, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(key) / 8
	a := append([]byte(nil), defaultIV...)
	r := append([]byte(nil), key...)
	buf := make([]byte, 16)
	for j := 0; j <= 5; j++ {
		for i := 1; i <= n; i++ {
			copy(buf[:8], a)
			copy(buf[8:], r[(i-1)*8:i*8])
			block.Encrypt(buf, buf)
			binary.BigEndian.PutUint64(a, binary.BigEndian.Uint64(buf[:8])^uint64(n*j+i))
			copy(r[(i-1)*8:i*8], buf[8:])
		}
	}
	return append(a, r...), nil
}

func cbcDecrypter(key []byte) (cipher.BlockMode, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewCBCDecrypter(block, make([]byte, aes.BlockSize)), nil
}

// unpad removes the padding of CBC data (RFC 1423), checking it is what it should be.
func unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errPadding
	}
	n := int(data[len(data)-1])
	if n == 0 || n > aes.BlockSize || n > len(data) ||
		!bytes.Equal(bytes.Repeat(data[len(data)-1:], n), data[len(data)-n:]) {
		return nil, errPadding
	}
	return data[:len(data)-n], nil
}

// decryptStream decrypts an AES-CBC file of size bytes from in into out, in chunks, padding removed;
// it gives the size decrypted.
func decryptStream(in io.Reader, size int64, key []byte, out io.Writer) (int64, error) {
	if size%aes.BlockSize != 0 {
		return 0, errBlocks
	}
	mode, err := cbcDecrypter(key)
	if err != nil {
		return 0, err
	}
	buf := make([]byte, chunkSize)
	var done, written int64
	for done < size {
		want := int64(chunkSize)
		if size-done < want {
			want = size - done
		}
		if _, err := io.ReadFull(in, buf[:want]); err != nil {
			return 0, err
		}
		done += want
		data := buf[:want]
		mode.CryptBlocks(data, data)
		if done == size { // the last chunk, which has the padding
			if data, err = unpad(data); err != nil {
				return 0, err
			}
		}
		if _, err := out.Write(data); err != nil {
			return 0, err
		}
		written += int64(len(data))
	}
	return written, nil // an empty file decrypts to nothing, as the library's chunked decryption
}

// decryptFile decrypts a file of the backup into a temporary file beside the output, then moves it
// to the output: the output is there whole or not at all. It gives the size decrypted.
func decryptFile(inPath string, key []byte, outPath string) (int64, error) {
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	in, err := os.Open(inPath)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, "tmp")
	if err != nil {
		return 0, err
	}
	hold(tmp.Name())
	defer release(tmp.Name())
	n, err := decryptStream(in, st.Size(), key, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), outPath)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return 0, err
	}
	return n, nil
}
