package importers

import (
	"reflect"
	"testing"
	"time"
)

// The Greek carriers' notices, read as everysaid/carriers/gr.py reads them (the expected values are
// what the Python gives for these invented notices): numbers, times in Greek time (an hour the
// clocks skip or say twice read as Python does), counts, busy.
func TestGreekCarrierNotices(t *testing.T) {
	sent := time.Date(2024, 1, 5, 10, 0, 0, 0, grTZ)
	type alert struct {
		number   string
		unix     int64
		attempts int
		busy     bool
	}
	for _, c := range []struct {
		text string
		want []alert
	}{
		{"ΕΙΧΑΤΕ 2 ΚΛΗΣΕΙΣ: 6941234567, 05/01 09:12 (2) ΚΑΙ 2101234567, 4/1 23:59",
			[]alert{{"6941234567", 1704438720, 2, false}, {"2101234567", 1704405540, 1, false}}},
		{"KΛHΣEIΣ: +30 694 123 4567, 31-12-23 22:10 3 ΦΟΡΕΣ", []alert{{"+306941234567", 1704053400, 3, false}}},
		{"ΔΩΡΕΑΝ ΕΝΗΜΕΡΩΣΗ: ΕΙΧΑΤΕ 1 ΚΛΗΣΗ ΑΠΟ ΤΟ 6977000111 ΣΤΙΣ 09:30,05/01/24 ΚΑΤΕΙΛΗΜΜΕΝΟΣ",
			[]alert{{"6977000111", 1704439800, 1, true}}},
		{"ΕΙΧΑΤΕ 3 ΚΛΗΣΕΙΣ: 6941234567 ΣΤΙΣ 06/01 09:12", []alert{{"6941234567", 1704525120, 3, false}}},
		{"καλημέρα 6941234567, 05/01 09:12", nil},
		{"ΕΙΧΑΤΕ 1 ΚΛΗΣΗ: 6941234567, 31/02 09:12, 6941234568, 30/01 25:12, 6941234569, 29/03/2026 03:30, 6941234560, 26/10/2025 03:30",
			[]alert{{"6941234569", 1774747800, 1, false}, {"6941234560", 1761438600, 1, false}}},
		{"Είχατε 1 κλήση: 6941234567, 10/12, 08:05", nil},
	} {
		var got []alert
		for _, a := range grAlerts(c.text, sent) {
			got = append(got, alert{a.Number, a.When.Unix(), a.Attempts, a.Busy})
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %v, want %v", c.text, got, c.want)
		}
	}
	if _, err := EnabledCarriers([]string{"gr", "xx"}); err == nil {
		t.Error("an unknown carrier is no error")
	}
}
