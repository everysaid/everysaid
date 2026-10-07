package archive_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"everysaid/internal/archive"
)

func BenchmarkAddMessage(b *testing.B) {
	a, err := archive.Open(filepath.Join(b.TempDir(), "a.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer a.Close()
	src := a.Source("bench", "/x", "", "")
	conv := a.Conversation("sms", []archive.Handle{archive.H("phone", "+15550001111")}, "", "")
	sender := a.Address(archive.H("phone", "+15550001111"))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.AddMessage(src, fmt.Sprint(i), archive.Message{Service: "sms", ConversationID: conv, TS: int64(i), SenderID: sender,
			Kind: "text", Text: fmt.Sprintf("καλημέρα φίλε μου %d", i)})
		a.HasOrigin(src, fmt.Sprint(i), "")
	}
	a.Commit()
}
