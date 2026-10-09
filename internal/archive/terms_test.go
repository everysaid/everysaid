package archive_test

import (
	"path/filepath"
	"strings"
	"testing"

	"everysaid/internal/archive"
	"everysaid/internal/db"
	"everysaid/internal/text"
)

// The words of `term` are split by the index's own tokenizer: the schema's message_fts has it.
func TestTokenizerIsTheIndexs(t *testing.T) {
	if !strings.Contains(archive.Schema, "detail=none, tokenize='"+archive.Tokenizer+"');") {
		t.Fatal("message_fts in schema.sql has another tokenizer than archive.Tokenizer")
	}
	got := strings.Join(archive.Tokens(text.Fold("Καλημέρα, e-mail! 😂 x² Ünïcode")), "|")
	if got != "καλημερα|e|mail|x2|unicode" { // x² folds to x2
		t.Fatal(got)
	}
	if n := len(archive.Tokens("!!! 😂 ...")); n != 0 {
		t.Fatal(n)
	}
}

// `term` is the index's word list: each message's words come in with it, a word no message has any
// more goes at SyncTerms, and SyncTerms puts back a word that went missing.
func TestTermsAreTheIndexsWords(t *testing.T) {
	a, err := archive.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	src := a.Source("test", "", "", "")
	conv := a.Conversation("sms", []archive.Handle{archive.H("phone", "+15550000001")}, "", "")
	add := func(ts int64, txt string) int64 {
		return a.AddMessage(src, txt, archive.Message{Service: "sms", ConversationID: conv, TS: ts, Kind: "text", Text: txt})
	}
	vocab := func() string {
		db.Exec(a.Tx(), "CREATE VIRTUAL TABLE IF NOT EXISTS temp.w USING fts5vocab(main, message_fts, row)")
		return strings.Join(db.Strs(a.Tx(), "SELECT term FROM temp.w ORDER BY term"), "|")
	}
	terms := func() string { return strings.Join(db.Strs(a.Tx(), "SELECT term FROM term ORDER BY term"), "|") }

	add(1000, "Καλημέρα κόσμε")
	id := add(2000, "e-mail μου")
	if terms() != vocab() || terms() != "e|καλημερα|κοσμε|μου|mail" && terms() != "e|mail|καλημερα|κοσμε|μου" {
		t.Fatalf("terms %s, index %s", terms(), vocab())
	}
	a.UnindexText(id)
	if added, removed := archive.SyncTerms(a.Tx()); added != 0 || removed != 3 || terms() != vocab() {
		t.Fatalf("after a message left the index: +%d -%d, terms %s, index %s", added, removed, terms(), vocab())
	}
	a.Exec("DELETE FROM term WHERE term = 'κοσμε'")
	if added, removed := archive.SyncTerms(a.Tx()); added != 1 || removed != 0 || terms() != vocab() {
		t.Fatalf("a word missing: +%d -%d, terms %s", added, removed, terms())
	}
}

// Words (split in Go) are Tokens (split by SQLite): on letters of many scripts, digits, marks,
// signs, symbols and emoji, alone and mixed.
func TestWordsAreTokens(t *testing.T) {
	samples := []string{"Καλημέρα, e-mail! 😂 x² Ünïcode", "नमस्ते दुनिया", "שָׁלוֹם", "مرحبا بالعالم", "こんにちは世界",
		"안녕하세요", "Привет, мир", "ﬁ ß ẞ İı ǅ", "1,5€ 12:30 #tag @name", "áb c̈d", "e‍moji👨‍👩‍👧", "ⅷ ① ½ ²",
		"private", "tab\tnew\nline", "ΣΊΣΥΦΟΣ ς"}
	for r := rune(0x20); r < 0x3000; r += 7 { // a stretch of everything, a few characters at a time
		samples = append(samples, string([]rune{r, r + 1, r + 2, ' ', 'a', r + 3, 'b'}))
	}
	for _, s := range samples {
		for _, v := range []string{s, text.Fold(s)} {
			if a, b := strings.Join(archive.Words(v), "|"), strings.Join(archive.Tokens(v), "|"); a != b {
				t.Errorf("%q: Words %q, Tokens %q", v, a, b)
			}
		}
	}
}
