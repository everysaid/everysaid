// Ports iphone_backup_decrypt/utils.py FilePlist: a Manifest.db file record, an NSKeyedArchiver
// binary plist.
package iphone

import (
	"errors"
	"fmt"

	"howett.net/plist"
)

// Record is what a file record of Manifest.db says of the file.
type Record struct {
	Size            int64
	ProtectionClass int64
	Mtime           int64  // LastModified, Unix seconds (0: not said)
	EncryptionKey   []byte // the wrapped key, nil for a file stored without one (empty, or a folder)
}

// archive is an NSKeyedArchiver plist: its objects and its root.
func archive(blob []byte) ([]any, map[string]any, error) {
	var top map[string]any
	if _, err := plist.Unmarshal(blob, &top); err != nil {
		return nil, nil, err
	}
	objects, ok := top["$objects"].([]any)
	if !ok {
		return nil, nil, errors.New("file record: no $objects")
	}
	return objects, top, nil
}

func object(objects []any, ref any) (any, error) {
	uid, ok := ref.(plist.UID)
	if !ok || uint64(uid) >= uint64(len(objects)) {
		return nil, fmt.Errorf("file record: bad reference %v", ref)
	}
	return objects[uid], nil
}

// integer is a plist number as int64 (plist gives uint64 or int64), ok false for anything else.
func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case uint64:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	}
	return 0, false
}

// ParseRecord reads a Manifest.db file record.
func ParseRecord(blob []byte) (*Record, error) {
	objects, top, err := archive(blob)
	if err != nil {
		return nil, err
	}
	t, _ := top["$top"].(map[string]any)
	root, err := object(objects, t["root"])
	if err != nil {
		return nil, err
	}
	data, ok := root.(map[string]any)
	if !ok {
		return nil, errors.New("file record: the root is not a dictionary")
	}
	r := &Record{}
	if r.Size, ok = integer(data["Size"]); !ok {
		return nil, errors.New("file record: no Size")
	}
	if r.ProtectionClass, ok = integer(data["ProtectionClass"]); !ok {
		return nil, errors.New("file record: no ProtectionClass")
	}
	r.Mtime, _ = integer(data["LastModified"])
	if ref, has := data["EncryptionKey"]; has {
		o, err := object(objects, ref)
		if err != nil {
			return nil, err
		}
		d, _ := o.(map[string]any)
		key, ok := d["NS.data"].([]byte)
		if !ok {
			return nil, errors.New("file record: EncryptionKey without NS.data")
		}
		if len(key) >= 4 {
			r.EncryptionKey = key[4:]
		} else {
			r.EncryptionKey = []byte{}
		}
	}
	return r, nil
}

// listedSize is the size iphone-ls reads: the Size of the record's second object, 0 when it has none.
func listedSize(blob []byte) (int64, error) {
	objects, _, err := archive(blob)
	if err != nil {
		return 0, err
	}
	if len(objects) < 2 {
		return 0, errors.New("file record: fewer than two objects")
	}
	d, ok := objects[1].(map[string]any)
	if !ok {
		return 0, errors.New("file record: the second object is not a dictionary")
	}
	size, _ := integer(d["Size"])
	return size, nil
}
