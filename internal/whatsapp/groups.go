package whatsapp

// The groups: their names, members and addressing.

// The members of the groups the account is in, for mentions (send.go) and the archive's members of
// its groups (importers.WhatsAppState): all of them once at each start (one query, as WhatsApp Web
// makes on connecting), and a group again when WhatsApp says its members changed. A group the
// account has left keeps its last members, marked by whatsapp_group.member = 0.

import (
	"context"
	"encoding/json"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"

	"everysaid/internal/db"
	"everysaid/internal/importers"
)

func jidString(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	return j.ToNonAD().String()
}

// storeGroup replaces a group's members with what WhatsApp says now.
func (store *Store) storeGroup(g *types.GroupInfo) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	group := g.JID.String()
	if _, err := tx.Exec(`INSERT INTO whatsapp_group (store, jid, name, addressing, member, updated_at) VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT (store, jid) DO UPDATE SET name = excluded.name, addressing = excluded.addressing, member = 1,
		updated_at = excluded.updated_at`, store.key, group, g.Name, string(g.AddressingMode), time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM whatsapp_group_member WHERE store = ? AND group_jid = ?", store.key, group); err != nil {
		return err
	}
	for _, p := range g.Participants {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO whatsapp_group_member (store, group_jid, jid, phone, lid, is_admin, is_super_admin)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, store.key, group, jidString(p.JID), jidString(p.PhoneNumber), jidString(p.LID),
			p.IsAdmin, p.IsSuperAdmin); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// refreshGroups reads all the groups the account is in; the others are marked as left.
func refreshGroups(client *whatsmeow.Client, store *Store, logger waLog.Logger) {
	groups, err := client.GetJoinedGroups(context.Background())
	if err != nil {
		logger.Warnf("Failed to get the groups: %v", err)
		return
	}
	in := map[string]bool{}
	for _, g := range groups {
		if err := store.storeGroup(g); err != nil {
			logger.Warnf("Failed to store group %s: %v", g.JID, err)
		}
		in[g.JID.String()] = true
	}
	var left []string
	func() {
		defer db.Recover(new(error))
		for _, jid := range db.Strs(store.db, "SELECT jid FROM whatsapp_group WHERE store = ? AND member", store.key) {
			if !in[jid] {
				left = append(left, jid)
			}
		}
	}()
	for _, jid := range left {
		store.exec("UPDATE whatsapp_group SET member = 0 WHERE store = ? AND jid = ?", store.key, jid)
	}
	logger.Infof("Members of %d groups", len(groups))
}

// handleGroupEvent follows changes of members: a group joined comes with them, a change in one is
// read again (rare, and exact whatever the change named people by).
func handleGroupEvent(client *whatsmeow.Client, store *Store, b *importers.WABatch, evt interface{}, logger waLog.Logger) {
	switch v := evt.(type) {
	case *events.JoinedGroup:
		if err := store.storeGroup(&v.GroupInfo); err != nil {
			logger.Warnf("Failed to store group: %v", err)
		}
		if v.Name != "" {
			store.storeChat(v.JID.String(), v.Name, time.Time{})
		}
		joined(client, b, v)
	case *events.GroupInfo:
		groupEvent(b, v)
		if len(v.Join) == 0 && len(v.Leave) == 0 && len(v.Promote) == 0 && len(v.Demote) == 0 && v.Name == nil {
			return
		}
		go func() {
			g, err := client.GetGroupInfo(context.Background(), v.JID)
			if err != nil {
				// left or removed: no longer allowed to read it
				store.exec("UPDATE whatsapp_group SET member = 0 WHERE store = ? AND jid = ?", store.key, v.JID.String())
				return
			}
			if err := store.storeGroup(g); err != nil {
				logger.Warnf("Failed to store group: %v", err)
			}
		}()
	}
}

// mentionJID is how a group names one of its members, given any of their jids or a number:
// "" when they are not in it.
func (store *Store) mentionJID(group types.JID, who string) string {
	var addressing string
	store.db.QueryRow("SELECT coalesce(addressing, '') FROM whatsapp_group WHERE store = ? AND jid = ?", store.key,
		group.String()).Scan(&addressing)
	var jid, phone, lid string
	err := store.db.QueryRow(`SELECT jid, coalesce(phone, ''), coalesce(lid, '') FROM whatsapp_group_member
		WHERE store = ? AND group_jid = ? AND ? IN (jid, phone, lid)`, store.key, group.String(), who).Scan(&jid, &phone, &lid)
	if err != nil {
		return ""
	}
	switch {
	case addressing == string(types.AddressingModeLID) && lid != "":
		return lid
	case addressing == string(types.AddressingModePN) && phone != "":
		return phone
	}
	return jid
}

// groupEvent adds what changed in a group, as the actions of a notice: who joined, was added, left
// or was removed, made admin or not, the name, the description, the timer, who may edit the
// group's info or send, whether joining needs approval, the group's end.
func groupEvent(b *importers.WABatch, v *events.GroupInfo) {
	sender, senderPN := "", ""
	if v.Sender != nil {
		sender = jidString(*v.Sender)
	}
	if v.SenderPN != nil {
		senderPN = jidString(*v.SenderPN)
	}
	// the one who made the change, by either of their jids (a LID, or their number's)
	isSender := func(who string) bool { return who == sender || (senderPN != "" && who == senderPN) }
	var actions []map[string]any
	people := func(t string, jids []types.JID) {
		for _, j := range jids {
			who := jidString(j)
			switch {
			case t == "added" && v.JoinReason == "invite":
				actions = append(actions, map[string]any{"type": "joined_link", "who": who})
			case t == "added" && (isSender(who) || sender == ""):
				actions = append(actions, map[string]any{"type": "joined", "who": who})
			case t == "removed" && isSender(who):
				actions = append(actions, map[string]any{"type": "left", "who": who})
			default:
				actions = append(actions, map[string]any{"type": t, "who": who})
			}
		}
	}
	people("added", v.Join)
	people("removed", v.Leave)
	for _, j := range v.Promote {
		actions = append(actions, map[string]any{"type": "admin", "who": jidString(j), "on": true})
	}
	for _, j := range v.Demote {
		actions = append(actions, map[string]any{"type": "admin", "who": jidString(j), "on": false})
	}
	if v.Name != nil {
		actions = append(actions, map[string]any{"type": "title", "title": v.Name.Name})
	}
	if v.Topic != nil {
		actions = append(actions, map[string]any{"type": "description", "text": v.Topic.Topic})
	}
	if v.Ephemeral != nil {
		seconds := uint32(0)
		if v.Ephemeral.IsEphemeral {
			seconds = v.Ephemeral.DisappearingTimer
		}
		actions = append(actions, map[string]any{"type": "timer", "seconds": seconds})
	}
	if v.Locked != nil {
		level := "members"
		if v.Locked.IsLocked {
			level = "admins"
		}
		actions = append(actions, map[string]any{"type": "access_info", "level": level})
	}
	if v.Announce != nil {
		actions = append(actions, map[string]any{"type": "announcements", "on": v.Announce.IsAnnounce})
	}
	// whether joining needs an admin's approval was changed (whatsmeow says it is required whichever
	// way it went, so not which)
	if v.MembershipApprovalMode != nil {
		actions = append(actions, map[string]any{"type": "approval"})
	}
	if v.NewInviteLink != nil {
		actions = append(actions, map[string]any{"type": "link_reset"})
	}
	if v.Delete != nil && v.Delete.Deleted {
		actions = append(actions, map[string]any{"type": "ended"})
	}
	if len(actions) == 0 {
		return
	}
	js, _ := json.Marshal(actions)
	b.GroupEvents = append(b.GroupEvents, importers.WAGroupEvent{Group: v.JID.String(), At: v.Timestamp.UnixMilli(),
		Sender: sender, Actions: string(js)})
}

// joined adds the account's joining a group: added by someone, or by an invite link.
func joined(client *whatsmeow.Client, b *importers.WABatch, v *events.JoinedGroup) {
	me := ownJID(client)
	if me == "" {
		return
	}
	sender := ""
	if v.Sender != nil {
		sender = jidString(*v.Sender)
	}
	byMe := sender == me || (v.SenderPN != nil && jidString(*v.SenderPN) == me)
	var actions []map[string]any
	if v.Type == "new" { // a group made: by whom, its name, and the owner added where someone else made it
		actions = append(actions, map[string]any{"type": "created", "title": v.Name})
	}
	switch {
	case v.Reason == "invite":
		actions = append(actions, map[string]any{"type": "joined_link", "who": me})
	case sender != "" && !byMe:
		actions = append(actions, map[string]any{"type": "added", "who": me})
	case v.Type != "new":
		actions = append(actions, map[string]any{"type": "joined", "who": me})
	}
	js, _ := json.Marshal(actions)
	at := time.Now() // the event has no time of its own, but a new group's creation
	if v.Type == "new" && !v.GroupCreated.IsZero() {
		at = v.GroupCreated
	}
	// WhatsApp may say it again (on connecting): kept once a day (a later joining again is another)
	b.GroupEvents = append(b.GroupEvents, importers.WAGroupEvent{Group: v.JID.String(), At: at.UnixMilli(), Sender: sender,
		Actions: string(js), OncePerDay: true})
}

// groupPicture adds a group's new (or removed) photo as a change of it.
func groupPicture(b *importers.WABatch, v *events.Picture) {
	if v.JID.Server != types.GroupServer {
		return
	}
	action := `[{"type":"avatar"}]`
	if v.Remove {
		action = `[{"removed":true,"type":"avatar"}]`
	}
	b.GroupEvents = append(b.GroupEvents, importers.WAGroupEvent{Group: v.JID.String(), At: v.Timestamp.UnixMilli(),
		Sender: jidString(v.Author), Actions: action})
}
