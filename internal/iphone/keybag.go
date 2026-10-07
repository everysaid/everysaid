// Ports iphone_backup_decrypt/utils.py BackupKeyBag: the keybag of Manifest.plist, unlocked with
// a key derived from the backup password.
package iphone

import (
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	wrapPassphrase    = 2
	maxDPICIterations = 20_000_000
	maxITERIterations = 1_000_000
)

// ErrUnsafe: a Manifest.plist whose key derivation looks malicious.
var ErrUnsafe = errors.New("unsafe backup")

// tlvValue is a keybag value: its bytes, and as a number when it has 4 of them.
type tlvValue struct {
	raw   []byte
	num   uint32
	isNum bool
}

func value(b []byte) tlvValue {
	if len(b) == 4 {
		return tlvValue{raw: b, num: binary.BigEndian.Uint32(b), isNum: true}
	}
	return tlvValue{raw: b}
}

type classKey map[string]tlvValue

type keybag struct {
	typ         uint32
	uuid, wrap  *tlvValue
	attrs       map[string]tlvValue
	classes     map[uint32]classKey
	order       []uint32 // the classes in the keybag's order
	keys        map[uint32][]byte
	unlocked    bool
	passwordKey []byte
}

func parseKeybag(data []byte) (*keybag, error) {
	k := &keybag{attrs: map[string]tlvValue{}, classes: map[uint32]classKey{}, keys: map[uint32][]byte{}}
	var current classKey
	store := func() error {
		if current == nil {
			return nil
		}
		c, ok := current["CLAS"]
		if !ok || !c.isNum {
			return errors.New("Unexpected BackupKeyBag format!")
		}
		if _, seen := k.classes[c.num]; !seen {
			k.order = append(k.order, c.num)
		}
		k.classes[c.num] = current
		return nil
	}
	for start := 0; start+8 <= len(data); {
		tag := string(data[start : start+4])
		length := int(binary.BigEndian.Uint32(data[start+4 : start+8]))
		end := start + 8 + length
		if end > len(data) || end < start {
			end = len(data)
		}
		v := value(data[start+8 : end])
		start = start + 8 + length
		switch {
		case tag == "TYPE":
			k.typ = v.num
			if !v.isNum {
				k.typ = 0
			}
			if k.typ > 3 {
				return nil, fmt.Errorf("Unexpected BackupKeyBag type! (%d > 3)", k.typ)
			}
		case tag == "UUID" && k.uuid == nil:
			k.uuid = &v // the first UUID is the keybag's
		case tag == "WRAP" && k.wrap == nil:
			k.wrap = &v // and so is the first WRAP
		case tag == "UUID": // the others begin a class key
			if err := store(); err != nil {
				return nil, err
			}
			current = classKey{"UUID": v}
		case tag == "CLAS" || tag == "WRAP" || tag == "WPKY" || tag == "KTYP" || tag == "PBKY":
			if current == nil {
				return nil, errors.New("Unexpected BackupKeyBag format!")
			}
			current[tag] = v
		default:
			k.attrs[tag] = v
		}
	}
	if err := store(); err != nil {
		return nil, err
	}
	return k, nil
}

func (k *keybag) iterations(name string, max uint32) (int, error) {
	v, ok := k.attrs[name]
	if !ok || !v.isNum || v.num < 1 || v.num > max {
		return 0, fmt.Errorf("%w: invalid BackupKeybag %s iteration count; expected an integer between 1 and %d", ErrUnsafe, name, max)
	}
	return int(v.num), nil
}

// deriveKey derives the key from the password: PBKDF2 with SHA-256 (DPSL, DPIC), then SHA-1
// (SALT, ITER).
func (k *keybag) deriveKey(password []byte) ([]byte, error) {
	dpic, err := k.iterations("DPIC", maxDPICIterations)
	if err != nil {
		return nil, err
	}
	iter, err := k.iterations("ITER", maxITERIterations)
	if err != nil {
		return nil, err
	}
	// without its salts the key would be derived with none, and every password said wrong
	for _, name := range []string{"DPSL", "SALT"} {
		if len(k.attrs[name].raw) == 0 {
			return nil, fmt.Errorf("BackupKeyBag has no %s", name)
		}
	}
	round1, err := pbkdf2.Key(sha256.New, string(password), k.attrs["DPSL"].raw, dpic, 32)
	if err != nil {
		return nil, err
	}
	return pbkdf2.Key(sha1.New, string(round1), k.attrs["SALT"].raw, iter, 32)
}

// unlockWithKey unwraps the class keys wrapped with the password; false when the key is wrong.
func (k *keybag) unlockWithKey(key []byte) (bool, error) {
	k.passwordKey = key
	for _, class := range k.order {
		data := k.classes[class]
		wpky, ok := data["WPKY"]
		if !ok {
			continue
		}
		wrap, ok := data["WRAP"]
		if !ok {
			return false, errors.New("Unexpected BackupKeyBag format!")
		}
		if wrap.num&wrapPassphrase != 0 {
			key, err := aesUnwrap(k.passwordKey, wpky.raw)
			if err != nil {
				return false, nil
			}
			k.keys[class] = key
		}
	}
	k.unlocked = true
	return true, nil
}

// unwrapForClass unwraps a file's key (from Manifest.db) with its protection class's key.
func (k *keybag) unwrapForClass(class int64, wrapped []byte) ([]byte, error) {
	if !k.unlocked {
		return nil, errors.New("BackupKeyBag must be unlocked before using this method!")
	}
	key, ok := k.keys[uint32(class)]
	if !ok || class < 0 || class > 0xffffffff {
		return nil, fmt.Errorf("Key for protection class %d not present in BackupKeyBag!", class)
	}
	if len(wrapped) != 0x28 {
		return nil, errors.New("Invalid wrapped file key length!")
	}
	return aesUnwrap(key, wrapped)
}
