package demo

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"everysaid/internal/archive"
)

// previews adds a few links with the previews their services sent (a page's picture, title and
// description; one without a picture), after everything else, so that the rest of the demo stays
// as it was. The pictures are drawn as the others, each of its own seed.
func previews(a *archive.Archive, source func(string) int64, people []*person, dir string, seed int64, now time.Time) error {
	list := []struct {
		service, url, site, title, description, text string
		outgoing, picture                            bool
		daysAgo                                      float64
	}{
		{"whatsapp", "https://example.org/syntages/fakes", "Συνταγές", "Φακές με δυόσμο",
			"Η κλασική σούπα του χειμώνα, με λίγο ξίδι στο τέλος.", "Δες αυτή, είναι εύκολη https://example.org/syntages/fakes",
			false, true, 5},
		{"telegram", "https://example.com/news/weekend-rain", "Example News", "Βροχές το Σαββατοκύριακο",
			"Η πρόγνωση για την Αθήνα και τα νησιά.", "https://example.com/news/weekend-rain", true, true, 6},
		{"viber", "https://example.net/concert", "Example Tickets", "Συναυλία στο Ηρώδειο",
			"Εισιτήρια από 20 €, για τις 14 Ιουνίου.", "Πάμε; https://example.net/concert", false, false, 7},
	}
	for i, l := range list {
		var who *person
		for _, p := range people {
			if contains(p.services, l.service) {
				who = p
				break
			}
		}
		if who == nil {
			continue
		}
		h := phone(who.number)
		conv := a.Conversation(l.service, []archive.Handle{h}, "", "")
		var sender int64
		if !l.outgoing {
			sender = a.Address(h)
		}
		p := &archive.Preview{URL: l.url, Site: l.site, Title: l.title, Description: l.description}
		if l.picture {
			f := filepath.Join(dir, fmt.Sprintf("preview%d.jpg", i+1))
			if err := picture(f, seed*1000+9000+int64(i), l.site); err != nil {
				return err
			}
			b, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			p.Image = b
			os.Remove(f)
		}
		ts := ms(now.Add(-days(l.daysAgo)))
		a.AddMessage(source(l.service), fmt.Sprintf("link%d", i+1), archive.Message{Service: l.service, ConversationID: conv,
			TS: ts, Outgoing: l.outgoing, SenderID: sender, Kind: "text", Text: l.text,
			Key: fmt.Sprintf("demo-%s-link%d", l.service, i+1), Extras: &archive.Extras{Subtype: "link", Preview: p}})
	}
	return nil
}
