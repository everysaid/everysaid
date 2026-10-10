// mautrix-signal - A Matrix-signal puppeting bridge.
// Copyright (C) 2024 Tulir Asokan
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package events

import (
	"github.com/google/uuid"

	"go.mau.fi/mautrix-signal/pkg/libsignalgo"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/protobuf/signalpb"
	"go.mau.fi/mautrix-signal/pkg/signalmeow/types"
)

type SignalEvent interface {
	isSignalEvent()
}

func (*ChatEvent) isSignalEvent()              {}
func (*DecryptionError) isSignalEvent()        {}
func (*Content) isSignalEvent()                {}
func (*Receipt) isSignalEvent()                {}
func (*ReadSelf) isSignalEvent()               {}
func (*Call) isSignalEvent()                   {}
func (*ContactList) isSignalEvent()            {}
func (*ACIFound) isSignalEvent()               {}
func (*DeleteForMe) isSignalEvent()            {}
func (*MessageRequestResponse) isSignalEvent() {}
func (*QueueEmpty) isSignalEvent()             {}
func (*LoggedOut) isSignalEvent()              {}

type MessageInfo struct {
	Sender uuid.UUID
	ChatID string

	GroupRevision   uint32
	ServerTimestamp uint64
}

type ChatEvent struct {
	Info  MessageInfo
	Event signalpb.ChatEventContent
}

type DecryptionError struct {
	Sender    uuid.UUID
	Err       error
	Timestamp uint64

	// everysaid: the sender's device, the group the message was in (sealed sender's, where known),
	// its content hint, and whether the sender was asked to send it again.
	SenderDevice uint
	GroupID      *libsignalgo.GroupIdentifier
	ContentHint  libsignalgo.UnidentifiedSenderMessageContentHint
	Retried      bool
}

// Content (everysaid) is every decrypted content whole, with its envelope, before signalmeow's own
// handling of it: for a client that reads the protobufs itself (call messages and the phone's call
// log, which signalmeow does not pass on, among them). The handler's false is a failure: the
// envelope is not acknowledged, and comes again.
type Content struct {
	Envelope     *signalpb.Envelope
	Content      *signalpb.Content
	Sender       libsignalgo.ServiceID
	SenderDevice uint
	Destination  libsignalgo.ServiceID
}

type Receipt struct {
	Sender  uuid.UUID
	Content *signalpb.ReceiptMessage
}

type ReadSelf struct {
	Timestamp uint64
	Messages  []*signalpb.SyncMessage_Read
}

type Call struct {
	Info      MessageInfo
	Timestamp uint64
	IsRinging bool
}

type ContactList struct {
	Contacts []*types.Recipient
	IsFromDB bool
}

type ACIFound struct {
	PNI libsignalgo.ServiceID
	ACI libsignalgo.ServiceID
}

type DeleteForMe struct {
	Timestamp uint64
	*signalpb.SyncMessage_DeleteForMe
}

type MessageRequestResponse struct {
	Timestamp uint64
	ThreadACI uuid.UUID
	GroupID   *libsignalgo.GroupIdentifier
	Type      signalpb.SyncMessage_MessageRequestResponse_Type
	Raw       *signalpb.SyncMessage_MessageRequestResponse
}

type QueueEmpty struct{}

type LoggedOut struct{ Error error }
