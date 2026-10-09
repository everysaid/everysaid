package archive

// The words of the search index. `message_fts` holds each message's text folded (text.Fold) as
// words, without their places (detail=none); `term` lists every word it holds, so that a search
// for a part of a word finds the words that contain it and then the messages that have them.
// Words are told by SQLite's own tokenizer, the index's, run on a small database in memory: the
// same code that splits a message for the index splits it for `term` and splits what is searched.
//
// `term` is kept with the index (IndexText); SyncTerms makes it the index's word list again
// (fts5vocab), adding a word that is missing and taking out one no message has any more, when the
// server starts and after an import. A word too many finds nothing; a word missing would hide
// messages.

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"everysaid/internal/db"
)

// Tokenizer is message_fts's tokenizer, as schema.sql writes it (a test holds them together).
const Tokenizer = "unicode61 remove_diacritics 2"

var tokens struct {
	sync.Mutex
	db *sql.DB
}

// Tokens are the words of a text as message_fts holds them, in order (a word as often as it is
// there); none for a text of nothing but spaces, signs and symbols.
func Tokens(s string) []string {
	tokens.Lock()
	defer tokens.Unlock()
	if tokens.db == nil {
		d, err := sql.Open("sqlite", "file::memory:")
		if err != nil {
			panic(&Error{Query: "tokenizer", Err: err})
		}
		d.SetMaxOpenConns(1) // the database in memory is the connection's: one, kept
		d.SetMaxIdleConns(1)
		d.SetConnMaxLifetime(0)
		d.SetConnMaxIdleTime(0)
		for _, q := range []string{
			fmt.Sprintf("CREATE VIRTUAL TABLE tok USING fts5(text, tokenize='%s')", Tokenizer),
			"CREATE VIRTUAL TABLE tok_words USING fts5vocab(tok, instance)",
		} {
			if _, err := d.Exec(q); err != nil {
				panic(&Error{Query: q, Err: err})
			}
		}
		tokens.db = d
	}
	if s == "" {
		return nil
	}
	db.Exec(tokens.db, "INSERT INTO tok (rowid, text) VALUES (1, ?)", s)
	out := db.Strs(tokens.db, "SELECT term FROM tok_words ORDER BY offset")
	db.Exec(tokens.db, "DELETE FROM tok")
	return out
}

// Words are the words of a text as Tokens gives them, split in Go: what each character is (part of
// a word, and as what, or a separator) is asked of the tokenizer once, then kept. The tokenizer
// takes characters one by one (unicode61: a character is of a word or not by its category, and is
// folded on its own), so the two agree; a test holds them together on many texts.
func Words(s string) []string {
	var out []string
	var b strings.Builder
	in := false
	for _, r := range s {
		c := classOf(r)
		if !c.word {
			if in && b.Len() > 0 {
				out = append(out, b.String())
			}
			b.Reset()
			in = false
			continue
		}
		in = true
		b.WriteString(c.as)
	}
	if in && b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// IsWordChar says whether the tokenizer takes the character as part of a word.
func IsWordChar(r rune) bool { return classOf(r).word }

type charClass struct {
	word bool   // part of a word
	as   string // as the word holds it (nothing for a mark the tokenizer takes off)
}

var classes sync.Map // rune -> charClass

func classOf(r rune) charClass {
	if c, ok := classes.Load(r); ok {
		return c.(charClass)
	}
	var c charClass
	if t := Tokens(string(r)); len(t) == 1 {
		c = charClass{word: true, as: t[0]}
	} else if len(Tokens("a"+string(r)+"a")) == 1 { // of a word, folded to nothing
		c = charClass{word: true}
	}
	classes.Store(r, c)
	return c
}

// IndexText puts a message's folded text into the search index and its words into `term`.
func (a *Archive) IndexText(messageID int64, folded string) {
	a.Exec("INSERT INTO message_fts (rowid, text) VALUES (?, ?)", messageID, folded)
	seen := map[string]bool{}
	for _, t := range Tokens(folded) {
		if !seen[t] {
			seen[t] = true
			a.Exec("INSERT OR IGNORE INTO term VALUES (?)", t)
		}
	}
}

// UnindexText takes a message out of the search index (its words stay in `term` until SyncTerms).
func (a *Archive) UnindexText(messageID int64) {
	a.Exec("DELETE FROM message_fts WHERE rowid = ?", messageID)
}

// SyncTerms makes `term` the index's list of words: those missing added, those no message has
// any more taken out. It gives how many of each.
func SyncTerms(q db.Querier) (added, removed int64) {
	db.Exec(q, "CREATE VIRTUAL TABLE IF NOT EXISTS temp.message_words USING fts5vocab(main, message_fts, row)")
	added = rows(db.Exec(q, "INSERT OR IGNORE INTO term SELECT term FROM temp.message_words"))
	removed = rows(db.Exec(q, "DELETE FROM term WHERE term NOT IN (SELECT term FROM temp.message_words)"))
	return added, removed
}

func rows(r sql.Result) int64 {
	n, _ := r.RowsAffected()
	return n
}
