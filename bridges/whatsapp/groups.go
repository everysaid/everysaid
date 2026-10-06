package main

// The members of the groups the account is in, for mentions (send.go) and for whoever reads the
// bridge: all of them once at each start (one query, as WhatsApp Web makes on connecting), and a
// group again when WhatsApp says its members changed. A group the account has left keeps its last
// members, marked by group_info.member = 0.

import (
	"context"
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
	case *events.GroupInfo:
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
