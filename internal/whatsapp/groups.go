package whatsapp

// Ports bridges/whatsapp/groups.go.

// The members of the groups the account is in, for mentions (send.go) and for whoever reads the
// bridge: all of them once at each start (one query, as WhatsApp Web makes on connecting), and a
// group again when WhatsApp says its members changed. A group the account has left keeps its last
// members, marked by group_info.member = 0.

import (
	"context"
	"encoding/json"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
)

func (store *MessageStore) migrateGroups() error {
	_, err := store.db.Exec(`
		CREATE TABLE IF NOT EXISTS group_info (
			jid TEXT PRIMARY KEY,
			name TEXT,
			addressing TEXT,          -- 'pn' or 'lid': how its members are named in messages and mentions
			member BOOLEAN,           -- the account is in it now
			updated_at TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS group_members (
			group_jid TEXT,
			jid TEXT,                 -- as the group names them (its addressing)
			phone TEXT,               -- their phone number's jid, where known
			lid TEXT,                 -- their LID, where known
			is_admin BOOLEAN,
			is_super_admin BOOLEAN,
			PRIMARY KEY (group_jid, jid)
		);
		-- what changed in a group, as WhatsApp said it: its actions as notices say them
		-- (docs/design.md, "Notices"), people by jid
		CREATE TABLE IF NOT EXISTS group_event (
			group_jid TEXT,
			timestamp TIMESTAMP,
			sender TEXT,              -- who made the change ("" where WhatsApp did not say)
			actions TEXT,             -- JSON
			PRIMARY KEY (group_jid, timestamp, actions)
		);
	`)
	return err
}

func jidString(j types.JID) string {
	if j.IsEmpty() {
		return ""
	}
	return j.ToNonAD().String()
}

// storeGroup replaces a group's members with what WhatsApp says now.
func (store *MessageStore) storeGroup(g *types.GroupInfo) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	group := g.JID.String()
	if _, err := tx.Exec(`INSERT INTO group_info (jid, name, addressing, member, updated_at) VALUES (?, ?, ?, 1, ?)
		ON CONFLICT (jid) DO UPDATE SET name = excluded.name, addressing = excluded.addressing, member = 1,
		updated_at = excluded.updated_at`, group, g.Name, string(g.AddressingMode), time.Now()); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM group_members WHERE group_jid = ?", group); err != nil {
		return err
	}
	for _, p := range g.Participants {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO group_members (group_jid, jid, phone, lid, is_admin, is_super_admin)
			VALUES (?, ?, ?, ?, ?, ?)`, group, jidString(p.JID), jidString(p.PhoneNumber), jidString(p.LID),
			p.IsAdmin, p.IsSuperAdmin); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// refreshGroups reads all the groups the account is in; the others are marked as left.
func refreshGroups(client *whatsmeow.Client, store *MessageStore, logger waLog.Logger) {
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
	rows, err := store.db.Query("SELECT jid FROM group_info WHERE member")
	if err != nil {
		return
	}
	var left []string
	for rows.Next() {
		var jid string
		if rows.Scan(&jid) == nil && !in[jid] {
			left = append(left, jid)
		}
	}
	rows.Close()
	for _, jid := range left {
		store.db.Exec("UPDATE group_info SET member = 0 WHERE jid = ?", jid)
	}
	logger.Infof("Members of %d groups", len(groups))
}

// handleGroupEvent follows changes of members: a group joined comes with them, a change in one is
// read again (rare, and exact whatever the change named people by).
func handleGroupEvent(client *whatsmeow.Client, store *MessageStore, evt interface{}, logger waLog.Logger) {
	switch v := evt.(type) {
	case *events.JoinedGroup:
		if err := store.storeGroup(&v.GroupInfo); err != nil {
			logger.Warnf("Failed to store group: %v", err)
		}
		store.storeJoined(client, v, logger)
	case *events.GroupInfo:
		store.storeGroupEvent(v, logger)
		if len(v.Join) == 0 && len(v.Leave) == 0 && len(v.Promote) == 0 && len(v.Demote) == 0 && v.Name == nil {
			return
		}
		go func() {
			g, err := client.GetGroupInfo(context.Background(), v.JID)
			if err != nil {
				// left or removed: no longer allowed to read it
				store.db.Exec("UPDATE group_info SET member = 0 WHERE jid = ?", v.JID.String())
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
func (store *MessageStore) mentionJID(group types.JID, who string) string {
	var addressing string
	store.db.QueryRow("SELECT coalesce(addressing, '') FROM group_info WHERE jid = ?", group.String()).Scan(&addressing)
	var jid, phone, lid string
	err := store.db.QueryRow(`SELECT jid, coalesce(phone, ''), coalesce(lid, '') FROM group_members
		WHERE group_jid = ? AND ? IN (jid, phone, lid)`, group.String(), who).Scan(&jid, &phone, &lid)
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

// storeGroupEvent keeps what changed in a group, as the actions of a notice: who joined, was added,
// left or was removed, made admin or not, the name, the description, the timer, who may edit the
// group's info or send, whether joining needs approval, the group's end.
func (store *MessageStore) storeGroupEvent(v *events.GroupInfo, logger waLog.Logger) {
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
	if _, err := store.db.Exec("INSERT OR IGNORE INTO group_event VALUES (?, ?, ?, ?)",
		v.JID.String(), v.Timestamp, sender, string(js)); err != nil {
		logger.Warnf("Failed to store a group's change: %v", err)
	}
}

// storeJoined keeps the account's joining a group: added by someone, or by an invite link.
func (store *MessageStore) storeJoined(client *whatsmeow.Client, v *events.JoinedGroup, logger waLog.Logger) {
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
	if _, err := store.db.Exec(`INSERT INTO group_event SELECT ?, ?, ?, ? WHERE NOT EXISTS
		(SELECT 1 FROM group_event WHERE group_jid = ? AND sender = ? AND actions = ? AND timestamp > ?)`,
		v.JID.String(), at, sender, string(js), v.JID.String(), sender, string(js), time.Now().Add(-24*time.Hour)); err != nil {
		logger.Warnf("Failed to store a group's change: %v", err)
	}
}

// storeGroupPicture keeps a group's new (or removed) photo as a change of it.
func (store *MessageStore) storeGroupPicture(v *events.Picture, logger waLog.Logger) {
	if v.JID.Server != types.GroupServer {
		return
	}
	action := `[{"type":"avatar"}]`
	if v.Remove {
		action = `[{"removed":true,"type":"avatar"}]`
	}
	if _, err := store.db.Exec("INSERT OR IGNORE INTO group_event VALUES (?, ?, ?, ?)", v.JID.String(), v.Timestamp,
		jidString(v.Author), action); err != nil {
		logger.Warnf("Failed to store a group's change: %v", err)
	}
}
