// A label shown in capitals (CSS uppercase): Greek capitals take no accents, and not every browser
// drops them by itself (only some, where the page says it is Greek). So the accents and breathings
// of Greek letters only are taken out here (tonos, oxia, varia, perispomeni, psili, dasia); the
// diaeresis stays (ΑΫΠΝΙΑ), and other scripts keep theirs (É stays É).
export function caps(s: string): string {
  return s.normalize("NFD").replace(/(\p{Script=Greek}\p{M}*?)[̀́̓̔͂]+/gu, "$1").normalize("NFC");
}
