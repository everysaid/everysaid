"""Text as the search index holds it: one form for every way of writing the same word.

`fold()` lower-cases (Unicode case folding, which also makes the final sigma ς a σ), takes off the
accents and other combining marks of every script (Greek tonos and dialytika, Latin accents), and
brings compatibility forms to one (ligatures, full-width letters). The index stores folded text and
a query is folded the same way, so `καλημερα` finds `Καλημέρα` and `φιλοσ` finds `φίλος`.
"""
import re
import unicodedata

_MARKS = re.compile(r"[̀-ͯ᪰-᫿᷀-᷿⃐-⃿︠-︯]")


def fold(s):
    if not s:
        return s
    s = unicodedata.normalize("NFKD", s.casefold())
    return unicodedata.normalize("NFC", _MARKS.sub("", s))


def query(q):
    """A user's search turned into an FTS5 query: each word folded and quoted (so that FTS5's own
    operators in it are taken as text), a trailing * kept as a prefix search; words are ANDed."""
    words = []
    for w in q.split():
        prefix = w.endswith("*")
        w = fold(w.rstrip("*")).replace('"', '""')
        if w:
            words.append(f'"{w}"' + ("*" if prefix else ""))
    return " ".join(words)


def _folded_with_map(s):
    """fold(s), and for each of its characters the index in s it came from."""
    out, where = [], []
    for i, c in enumerate(s):
        f = fold(c)
        out.append(f)
        where.extend([i] * len(f))
    return "".join(out), where


class Matcher:
    """Where a search's words are in a text. case: exact (case and accents as typed); else folded.
    whole: whole words only; else anywhere, inside words too."""

    def __init__(self, q, case=False, whole=False):
        self.case, self.whole = case, whole
        self.words = [w for w in (q or "").split() if w]
        self.needles = [w if case else fold(w) for w in self.words]

    def spans(self, text):
        """[(start, end)] in `text` of every match of every word, in order."""
        if not text or not self.needles:
            return []
        hay, where = (text, None) if self.case else _folded_with_map(text)
        out = []
        for n in self.needles:
            for m in re.finditer(re.escape(n), hay):
                a, b = m.start(), m.end()
                if self.whole and ((a > 0 and hay[a - 1].isalnum()) or (b < len(hay) and hay[b].isalnum())):
                    continue
                out.append((a, b) if where is None else (where[a], where[b - 1] + 1))
        return sorted(out)

    def matches(self, text):
        """Whether every word is in the text."""
        return all(self.__class__(w, self.case, self.whole).spans(text) for w in self.words)
