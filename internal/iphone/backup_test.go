package iphone

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"howett.net/plist"

	"everysaid/internal/config"
	"everysaid/internal/db"
	"everysaid/internal/phones"
)

const testPassword = "correct horse"

// testFile is a file of a test backup; nil data with key false: stored without a key (empty).
type testFile struct {
	domain, path string
	data         []byte
	noKey        bool
	mtime        int64
	claimed      int64 // the size the manifest says, when not len(data)
}

func tlv(tag string, data []byte) []byte {
	b := make([]byte, 8, 8+len(data))
	copy(b, tag)
	binary.BigEndian.PutUint32(b[4:], uint32(len(data)))
	return append(b, data...)
}

func u32(n uint32) []byte { return binary.BigEndian.AppendUint32(nil, n) }

func fill(n int, b byte) []byte { return bytes.Repeat([]byte{b}, n) }

// record is an NSKeyedArchiver file record, as Manifest.db keeps it.
func record(t *testing.T, size, class, mtime int64, wrappedKey []byte) []byte {
	file := map[string]any{"$class": plist.UID(3), "Size": size, "ProtectionClass": class, "LastModified": mtime,
		"RelativePath": plist.UID(2), "Mode": int64(0o100644)}
	objects := []any{"$null", file, "a/path", map[string]any{"$classname": "MBFile", "$classes": []any{"MBFile", "NSObject"}}}
	if wrappedKey != nil {
		file["EncryptionKey"] = plist.UID(len(objects))
		objects = append(objects, map[string]any{"NS.data": append(binary.LittleEndian.AppendUint32(nil, uint32(class)), wrappedKey...),
			"$class": plist.UID(len(objects) + 1)})
		objects = append(objects, map[string]any{"$classname": "NSMutableData", "$classes": []any{"NSMutableData", "NSData", "NSObject"}})
	}
	b, err := plist.Marshal(map[string]any{"$version": int64(100000), "$archiver": "NSKeyedArchiver",
		"$top": map[string]any{"root": plist.UID(1)}, "$objects": objects}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// makeBackup writes a small encrypted backup into dir/<udid>: a keybag whose class keys are
// wrapped with the key derived from testPassword (few iterations), an encrypted Manifest.db, the
// files encrypted with their own keys.
func makeBackup(t *testing.T, dir string, files []testFile) string {
	t.Helper()
	bk := filepath.Join(dir, "00008000-TEST")
	if err := os.MkdirAll(bk, 0o700); err != nil {
		t.Fatal(err)
	}
	bag := append(tlv("VERS", u32(3)), tlv("TYPE", u32(1))...)
	bag = append(bag, tlv("UUID", fill(16, 1))...)
	bag = append(bag, tlv("HMCK", fill(40, 2))...)
	bag = append(bag, tlv("WRAP", u32(0))...)
	bag = append(bag, tlv("SALT", fill(20, 3))...)
	bag = append(bag, tlv("ITER", u32(7))...)
	bag = append(bag, tlv("DPWT", u32(1))...)
	bag = append(bag, tlv("DPIC", u32(11))...)
	bag = append(bag, tlv("DPSL", fill(20, 4))...)
	probe, err := parseKeybag(bag)
	if err != nil {
		t.Fatal(err)
	}
	passKey, err := probe.deriveKey([]byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	classKeys := map[int64][]byte{}
	for _, class := range []int64{1, 2, 3, 4} {
		classKeys[class] = fill(32, byte(0x40+class))
		wpky, _ := aesWrap(passKey, classKeys[class])
		bag = append(bag, tlv("UUID", fill(16, byte(0x10+class)))...)
		bag = append(bag, tlv("CLAS", u32(uint32(class)))...)
		bag = append(bag, tlv("WRAP", u32(3))...)
		bag = append(bag, tlv("KTYP", u32(0))...)
		bag = append(bag, tlv("WPKY", wpky)...)
	}

	// Manifest.db, plain first
	plain := filepath.Join(dir, "manifest-plain.db")
	m, err := db.Open(plain)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(m, "CREATE TABLE Files (fileID TEXT PRIMARY KEY, domain TEXT, relativePath TEXT, flags INTEGER, file BLOB)")
	db.Exec(m, "INSERT INTO Files VALUES ('0000000000000000000000000000000000000000', 'HomeDomain', 'Library', 2, ?)",
		record(t, 0, 0, 0, nil))
	for i, f := range files {
		sum := sha1.Sum([]byte(f.domain + "-" + f.path))
		id := hex.EncodeToString(sum[:])
		size := int64(len(f.data))
		if f.claimed != 0 {
			size = f.claimed
		}
		var wrapped []byte
		class := int64(i%3 + 1)
		if !f.noKey {
			fileKey := fill(32, byte(i+1))
			wrapped, _ = aesWrap(classKeys[class], fileKey)
			if err := os.MkdirAll(filepath.Join(bk, id[:2]), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bk, id[:2], id), encrypt(fileKey, f.data), 0o600); err != nil {
				t.Fatal(err)
			}
		} else {
			os.MkdirAll(filepath.Join(bk, id[:2]), 0o700)
			os.WriteFile(filepath.Join(bk, id[:2], id), nil, 0o600)
		}
		db.Exec(m, "INSERT INTO Files VALUES (?, ?, ?, 1, ?)", id, f.domain, f.path, record(t, size, class, f.mtime, wrapped))
	}
	m.Close()
	raw, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(plain)
	manifestKey := fill(32, 0x77)
	wrapped, _ := aesWrap(classKeys[4], manifestKey)
	if err := os.WriteFile(filepath.Join(bk, "Manifest.db"), encrypt(manifestKey, raw), 0o600); err != nil {
		t.Fatal(err)
	}
	mp, err := plist.Marshal(map[string]any{"IsEncrypted": true, "BackupKeyBag": bag,
		"ManifestKey": append(binary.LittleEndian.AppendUint32(nil, 4), wrapped...), "Version": "10.0"}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bk, "Manifest.plist"), mp, 0o600); err != nil {
		t.Fatal(err)
	}
	return bk
}

// sqliteFile is the bytes of a SQLite database made by the statements.
func sqliteFile(t *testing.T, stmts ...string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		db.Exec(d, s)
	}
	d.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// isolate moves the config, data and cache folders into the test's own.
func isolate(t *testing.T) string {
	dir := t.TempDir()
	for _, v := range []string{"EVERYSAID_DATA", "EVERYSAID_CACHE", "EVERYSAID_CONFIG", "EVERYSAID_STATE"} {
		t.Setenv(v, filepath.Join(dir, strings.ToLower(strings.TrimPrefix(v, "EVERYSAID_"))))
	}
	t.Setenv("EVERYSAID_KEYRING", "everysaid-test-none")
	config.Load()
	t.Cleanup(config.Load)
	return dir
}

func TestOpenAndExtract(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	big := bytes.Repeat([]byte("0123456789abcdef"), 102400) // more than one chunk, whole pages
	bk := makeBackup(t, dir, []testFile{
		{domain: "HomeDomain", path: "Library/SMS/sms.db", data: []byte("sms data"), mtime: 1700000000},
		{domain: "AppDomain-x", path: "Documents/big.bin", data: big, claimed: 4096},
		{domain: "AppDomain-x", path: "Documents/empty", noKey: true},
	})
	if _, err := Open(bk, []byte("wrong")); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("a wrong password: %v", err)
	}
	b, err := Open(bk, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	tmp := b.tmp
	out := filepath.Join(t.TempDir(), "o", "sms.db")
	if err := b.ExtractFile("Library/SMS/sms.db", "", out, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); string(got) != "sms data" {
		t.Errorf("sms.db: %q", got)
	}
	if st, _ := os.Stat(out); st.ModTime().Unix() != 1700000000 {
		t.Errorf("mtime %v", st.ModTime())
	}
	var told [2]int64
	if err := b.ExtractFile("Documents/big.bin", "AppDomain-%", out+".big", func(n, c int64) { told = [2]int64{n, c} }); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out + ".big"); !bytes.Equal(got, big) || told != [2]int64{int64(len(big)), 4096} {
		t.Errorf("big.bin: %d bytes, told %v", len(got), told)
	}
	if err := b.ExtractFile("Library/SMS/sms.db", "AppDomain-%", out, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("another domain: %v", err)
	}
	// opened again with the key, without the password
	b2, err := OpenWithKey(bk, b.PasswordKey())
	if err != nil {
		t.Fatal(err)
	}
	b2.Close()

	groups, err := List(b, "%", "%", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []Group{{"AppDomain-x", "Documents", 2, 4096}, {"HomeDomain", "Library", 1, 8}}
	if len(groups) != 2 || groups[0] != want[0] || groups[1] != want[1] {
		t.Errorf("List: %+v", groups)
	}
	v, err := Verify(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	// big.bin decrypts to other than the 4096 claimed, but both are whole pages: a live database
	if v.Files != 3 || v.OK != 1 || v.Empty != 1 || len(v.Live) != 1 || len(v.Problems) != 0 {
		t.Errorf("Verify: %+v", v)
	}
	b.Close()
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("the decrypted manifest is still there: %v", err)
	}
}

func TestVerifyProblems(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	bk := makeBackup(t, dir, []testFile{
		{domain: "D", path: "a", data: []byte("abc"), claimed: 5},
		{domain: "D", path: "b", data: []byte("abcdef")},
	})
	// b's file goes missing
	sum := sha1.Sum([]byte("D-b"))
	id := hex.EncodeToString(sum[:])
	os.Remove(filepath.Join(bk, id[:2], id))
	b, err := Open(bk, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	v, err := Verify(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Problems) != 2 || v.Problems[0].Text != "size {n} instead of {size}: {name}" || v.Problems[1].Text != "missing: {name}" {
		t.Errorf("Verify: %+v", v.Problems)
	}
}

func TestNotEncrypted(t *testing.T) {
	dir := t.TempDir()
	mp, _ := plist.Marshal(map[string]any{"IsEncrypted": false}, plist.XMLFormat)
	os.WriteFile(filepath.Join(dir, "Manifest.plist"), mp, 0o600)
	os.WriteFile(filepath.Join(dir, "Manifest.db"), nil, 0o600)
	if _, err := Open(dir, []byte("x")); !errors.Is(err, ErrNotEncrypted) {
		t.Errorf("%v", err)
	}
}

func TestUnsafeIterations(t *testing.T) {
	bag := append(tlv("DPIC", u32(maxDPICIterations+1)), tlv("ITER", u32(1))...)
	k, err := parseKeybag(bag)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.deriveKey([]byte("x")); !errors.Is(err, ErrUnsafe) {
		t.Errorf("%v", err)
	}
	if _, err := parseKeybag(tlv("TYPE", u32(4))); err == nil {
		t.Error("a keybag of type 4 was taken")
	}
	if _, err := parseKeybag(tlv("WPKY", fill(40, 0))); err == nil {
		t.Error("a class key before any UUID was taken")
	}
}

func TestParseRecord(t *testing.T) {
	r, err := ParseRecord(record(t, 1234, 3, 1600000000, fill(40, 9)))
	if err != nil {
		t.Fatal(err)
	}
	if r.Size != 1234 || r.ProtectionClass != 3 || r.Mtime != 1600000000 || !bytes.Equal(r.EncryptionKey, fill(40, 9)) {
		t.Errorf("%+v", r)
	}
	r, err = ParseRecord(record(t, 0, 4, 0, nil))
	if err != nil || r.EncryptionKey != nil {
		t.Errorf("no key: %+v %v", r, err)
	}
	if n, err := listedSize(record(t, 99, 1, 0, nil)); err != nil || n != 99 {
		t.Errorf("listedSize %d %v", n, err)
	}
	if _, err := ParseRecord([]byte("not a plist")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestSync(t *testing.T) {
	base := isolate(t)
	root := filepath.Join(base, "backups")
	wa := sqliteFile(t, "CREATE TABLE ZWAMEDIAITEM (ZMEDIALOCALPATH TEXT)",
		"INSERT INTO ZWAMEDIAITEM VALUES ('Media/chat/one.jpg'), ('Media/chat/two.jpg'), (NULL)")
	vb := sqliteFile(t, "CREATE TABLE ZATTACHMENT (ZNAME TEXT)", "INSERT INTO ZATTACHMENT VALUES ('pic.jpg'), (NULL)")
	const waDomain = "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"
	bk := makeBackup(t, root, []testFile{
		{domain: "HomeDomain", path: "Library/SMS/sms.db", data: []byte("sms")},
		{domain: "HomeDomain", path: "Library/CallHistoryDB/CallHistory.storedata", data: []byte("calls")},
		{domain: "AppDomainGroup-group.viber.share.container", path: "com.viber/database/Contacts.data", data: vb},
		{domain: waDomain, path: "ChatStorage.sqlite", data: wa},
		{domain: waDomain, path: "ContactsV2.sqlite", data: []byte("contacts")},
		{domain: waDomain, path: "CallHistory.sqlite", data: []byte("wa calls")},
		{domain: waDomain, path: "Message/Media/chat/one.jpg", data: []byte("one")},
		{domain: waDomain, path: "Message/Media/chat/two.jpg", data: []byte("two")},
		{domain: waDomain, path: "Message/Media/chat/one.thumb", data: []byte("thumb")},
		{domain: "AppDomain-com.viber", path: "Documents/Attachments/pic.jpg", data: []byte("pic")},
		{domain: "AppDomain-com.viber", path: "Documents/Other/pic.jpg", data: []byte("other")},
	})
	// the archive already took two.jpg
	os.MkdirAll(config.Data, 0o700)
	a, _ := db.Open(filepath.Join(config.Data, "archive.db"))
	db.Exec(a, "CREATE TABLE source (id INTEGER PRIMARY KEY, name TEXT)")
	db.Exec(a, "CREATE TABLE attachment (source_id INTEGER, source_path TEXT)")
	db.Exec(a, "INSERT INTO source VALUES (1, 'iphone/whatsapp')")
	db.Exec(a, "INSERT INTO attachment VALUES (1, 'chat/two.jpg')")
	a.Close()

	out := filepath.Join(base, "out")
	os.MkdirAll(out, 0o700)
	os.WriteFile(filepath.Join(out, "sms.db-wal"), []byte("old"), 0o600)
	var lines []string
	say := func(text string, params map[string]any) { lines = append(lines, text) }
	err := Sync(SyncOptions{Out: out, BackupRoot: root, UDID: filepath.Base(bk), NoBackup: true,
		Password: []byte("wrong"), Say: say})
	if err == nil || err.Error() != "Wrong backup password." {
		t.Fatalf("a wrong password: %v", err)
	}
	err = Sync(SyncOptions{Out: out, BackupRoot: root, UDID: filepath.Base(bk), NoBackup: true,
		Password: []byte(testPassword), Say: say})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"sms.db": "sms", "CallHistory.storedata": "calls",
		"whatsapp-contacts.sqlite": "contacts", "whatsapp-calls.sqlite": "wa calls",
		"whatsapp-media/chat/one.jpg": "one", "viber-media/Attachments/pic.jpg": "pic"} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q %v", name, got, err)
		}
		if st, _ := os.Stat(filepath.Join(out, name)); st != nil && st.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %o", name, st.Mode().Perm())
		}
	}
	for _, gone := range []string{"sms.db-wal", "whatsapp-media/chat/two.jpg", "whatsapp-media/chat/one.thumb", "sms.db.part"} {
		if _, err := os.Stat(filepath.Join(out, gone)); err == nil {
			t.Errorf("%s is there", gone)
		}
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "OK {folder}: {new} new, {skipped} already in the archive, {total} in all") ||
		!strings.Contains(joined, "Done: {out}") {
		t.Errorf("lines: %s", joined)
	}

	// --only: those databases, no media
	only := filepath.Join(base, "only")
	if err := Sync(SyncOptions{Out: only, BackupRoot: root, UDID: filepath.Base(bk), NoBackup: true,
		Password: []byte(testPassword), Only: []string{"whatsapp-calls.sqlite"}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(only)
	if len(entries) != 1 || entries[0].Name() != "whatsapp-calls.sqlite" {
		t.Errorf("--only: %v", entries)
	}

	// no backup yet
	err = Sync(SyncOptions{Out: only, BackupRoot: root, UDID: "nothing", NoBackup: true, Password: []byte("x")})
	if err == nil || !strings.Contains(err.Error(), "no complete backup yet") {
		t.Errorf("no backup: %v", err)
	}
}

func TestSyncMainArgs(t *testing.T) {
	isolate(t)
	var out bytes.Buffer
	err := SyncMain([]string{"--help"}, &out)
	if err == nil || !strings.Contains(out.String(), "--save-password") {
		t.Errorf("help: %v %s", err, out.String())
	}
	if err := SyncMain([]string{"--bogus"}, &out); err == nil || !strings.Contains(err.Error(), "unrecognized arguments") {
		t.Errorf("bogus: %v", err)
	}
}

// TestBackupStep runs a fake idevicebackup2: its output reaches Raw as it was written (a progress
// bar's \r kept), what it writes is private (umask 077), and its failure is said in words.
func TestBackupStep(t *testing.T) {
	base := isolate(t)
	root := filepath.Join(base, "backups")
	bk := makeBackup(t, root, []testFile{
		{domain: "HomeDomain", path: "Library/SMS/sms.db", data: []byte("sms")},
	})
	bin := filepath.Join(base, "bin")
	os.MkdirAll(bin, 0o700)
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do last=$a; done\n" +
		"touch \"$last/made-by-backup\"\n" +
		"printf 'Receiving files\\n[==  ] 50%%\\r[====] 100%%\\n'\n" +
		"if [ -e \"$last/fail\" ]; then echo 'ERROR: No device found.' >&2; exit 1; fi\n"
	os.WriteFile(filepath.Join(bin, "idevicebackup2"), []byte(script), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var raw bytes.Buffer
	var lines []string
	say := func(text string, params map[string]any) { lines = append(lines, text) }
	err := Sync(SyncOptions{Out: filepath.Join(base, "out"), BackupRoot: root, UDID: filepath.Base(bk),
		Password: []byte(testPassword), Only: []string{"sms.db"}, Say: say, Raw: &raw})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw.String(), "[==  ] 50%\r[====] 100%\n") {
		t.Errorf("raw %q", raw.String())
	}
	if st, err := os.Stat(filepath.Join(root, "made-by-backup")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the backup's file: %v %v", st, err)
	}
	if len(lines) < 2 || lines[0] != "Backup: {cmd}" || lines[1] != "The iPhone may ask for its passcode: type it there." {
		t.Errorf("lines %q", lines)
	}

	os.WriteFile(filepath.Join(root, "fail"), nil, 0o600)
	err = Sync(SyncOptions{Out: filepath.Join(base, "out"), BackupRoot: root, UDID: filepath.Base(bk),
		Password: []byte(testPassword), Only: []string{"sms.db"}})
	if err == nil || err.Error() != "The backup failed: no iPhone found. Connect it with a cable, unlock it and tap Trust." {
		t.Errorf("a failed backup: %v", err)
	}
}

// TestExtractEmptyFile: iOS keeps an empty file without a key; it comes out empty, not as an error.
func TestExtractEmptyFile(t *testing.T) {
	isolate(t)
	bk := makeBackup(t, t.TempDir(), []testFile{{domain: "D", path: "empty", noKey: true, mtime: 1700000000}})
	b, err := Open(bk, []byte(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	out := filepath.Join(t.TempDir(), "x", "empty")
	if err := b.ExtractFile("empty", "", out, nil); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() != 0 || st.Mode().Perm() != 0o600 || st.ModTime().Unix() != 1700000000 {
		t.Errorf("%v %v", st, err)
	}
}

// TestKeybagWithoutSalt: a keybag missing a salt is said so, not taken as a wrong password.
func TestKeybagWithoutSalt(t *testing.T) {
	for _, missing := range []string{"DPSL", "SALT"} {
		var bag []byte
		for _, tag := range []string{"DPSL", "SALT"} {
			if tag != missing {
				bag = append(bag, tlv(tag, fill(20, 1))...)
			}
		}
		bag = append(bag, tlv("DPIC", u32(1))...)
		bag = append(bag, tlv("ITER", u32(1))...)
		k, err := parseKeybag(bag)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.deriveKey([]byte("x")); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: %v", missing, err)
		}
	}
}

// TestNotEncryptedSaid: a backup made without a password is said in the user's words, with what to do.
func TestNotEncryptedSaid(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	dir := filepath.Join(root, "00008000-PLAIN")
	os.MkdirAll(dir, 0o700)
	mp, _ := plist.Marshal(map[string]any{"IsEncrypted": false}, plist.XMLFormat)
	os.WriteFile(filepath.Join(dir, "Manifest.plist"), mp, 0o600)
	os.WriteFile(filepath.Join(dir, "Manifest.db"), nil, 0o600)
	err := Sync(SyncOptions{Out: t.TempDir(), BackupRoot: root, UDID: "00008000-PLAIN", NoBackup: true, Password: []byte("x")})
	var f *phones.Failure
	if !errors.As(err, &f) || !strings.HasPrefix(f.Text, "The backup is not encrypted") {
		t.Errorf("%v", err)
	}
}

// TestSyncSkipsWhatTheGivenArchiveHas: the files not copied again are those of the archive the
// app serves, not of the default one.
func TestSyncSkipsWhatTheGivenArchiveHas(t *testing.T) {
	base := isolate(t)
	root := filepath.Join(base, "backups")
	wa := sqliteFile(t, "CREATE TABLE ZWAMEDIAITEM (ZMEDIALOCALPATH TEXT)",
		"INSERT INTO ZWAMEDIAITEM VALUES ('Media/one.jpg'), ('Media/two.jpg')")
	vb := sqliteFile(t, "CREATE TABLE ZATTACHMENT (ZNAME TEXT)")
	const waDomain = "AppDomainGroup-group.net.whatsapp.WhatsApp.shared"
	files := []testFile{{domain: waDomain, path: "Message/Media/one.jpg", data: []byte("one")},
		{domain: waDomain, path: "Message/Media/two.jpg", data: []byte("two")},
		{domain: "AppDomainGroup-group.viber.share.container", path: "com.viber/database/Contacts.data", data: vb},
		{domain: waDomain, path: "ChatStorage.sqlite", data: wa}}
	for _, f := range []struct{ rel, domain string }{{"Library/SMS/sms.db", "HomeDomain"},
		{"Library/CallHistoryDB/CallHistory.storedata", "HomeDomain"}, {"ContactsV2.sqlite", waDomain}, {"CallHistory.sqlite", waDomain}} {
		files = append(files, testFile{domain: f.domain, path: f.rel, data: []byte("x")})
	}
	bk := makeBackup(t, root, files)
	other := filepath.Join(base, "other.db")
	a, _ := db.Open(other)
	db.Exec(a, "CREATE TABLE source (id INTEGER PRIMARY KEY, name TEXT)")
	db.Exec(a, "CREATE TABLE attachment (source_id INTEGER, source_path TEXT)")
	db.Exec(a, "INSERT INTO source VALUES (1, 'iphone/whatsapp')")
	db.Exec(a, "INSERT INTO attachment VALUES (1, 'two.jpg')")
	a.Close()
	out := filepath.Join(base, "out")
	if err := Sync(SyncOptions{Out: out, BackupRoot: root, UDID: filepath.Base(bk), NoBackup: true,
		Password: []byte(testPassword), Archive: other}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "whatsapp-media", "one.jpg")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(out, "whatsapp-media", "two.jpg")); err == nil {
		t.Error("two.jpg, which the given archive has, was copied")
	}
}

// TestOnlyUnknown: --only with a name that is not one of the databases says so, before anything.
func TestOnlyUnknown(t *testing.T) {
	isolate(t)
	err := Sync(SyncOptions{Out: t.TempDir(), BackupRoot: t.TempDir(), UDID: "x", NoBackup: true,
		Password: []byte("x"), Only: []string{"whatsapp-call.sqlite"}})
	if err == nil || !strings.HasPrefix(err.Error(), "Not one of the databases: whatsapp-call.sqlite (sms.db, ") {
		t.Errorf("%v", err)
	}
}
