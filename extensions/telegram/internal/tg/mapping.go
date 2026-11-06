package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Telegram's objects in the connector's words (pure: tested on made-up objects, mapping_test.go).

// entities are the users and chats a response carries, by id.
type entities struct {
	users map[int64]*tg.User
	chats map[int64]tg.ChatClass
}

func entitiesOf(users []tg.UserClass, chats []tg.ChatClass) entities {
	e := entities{users: map[int64]*tg.User{}, chats: map[int64]tg.ChatClass{}}
	for _, u := range users {
		if u, ok := u.(*tg.User); ok {
			e.users[u.ID] = u
		}
	}
	for _, c := range chats {
		e.chats[c.GetID()] = c
	}
	return e
}

// peerOf is how to address a peer, with the access hash the entities carry.
func (e entities) peerOf(p tg.PeerClass) (peer, bool) {
	switch p := p.(type) {
	case *tg.PeerUser:
		u := e.users[p.UserID]
		if u == nil {
			return peer{Kind: 'u', ID: p.UserID}, false
		}
		return peer{Kind: 'u', ID: u.ID, Hash: u.AccessHash}, true
	case *tg.PeerChat:
		return peer{Kind: 'g', ID: p.ChatID}, true
	case *tg.PeerChannel:
		if ch, ok := e.chats[p.ChannelID].(*tg.Channel); ok {
			return peer{Kind: 'c', ID: ch.ID, Hash: ch.AccessHash}, true
		}
		return peer{Kind: 'c', ID: p.ChannelID}, false
	}
	return peer{}, false
}

// membersOf is how many are in a group or channel, as far as its object says: always for a basic group, only
// sometimes for a supergroup or a channel (0 then).
func (e entities) membersOf(p tg.PeerClass) int {
	switch p := p.(type) {
	case *tg.PeerChat:
		if c, ok := e.chats[p.ChatID].(*tg.Chat); ok {
			return c.ParticipantsCount
		}
	case *tg.PeerChannel:
		if c, ok := e.chats[p.ChannelID].(*tg.Channel); ok {
			n, _ := c.GetParticipantsCount()
			return n
		}
	}
	return 0
}

// nameOf is how a peer shows: a person's name, a group's title.
func (e entities) nameOf(p tg.PeerClass) string {
	switch p := p.(type) {
	case *tg.PeerUser:
		return userName(e.users[p.UserID])
	case *tg.PeerChat:
		if c, ok := e.chats[p.ChatID].(*tg.Chat); ok {
			return c.Title
		}
	case *tg.PeerChannel:
		if c, ok := e.chats[p.ChannelID].(*tg.Channel); ok {
			return c.Title
		}
	}
	return ""
}

func userName(u *tg.User) string {
	if u == nil {
		return ""
	}
	if u.Deleted {
		return "已注销的账号"
	}
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if n == "" && u.Username != "" {
		n = "@" + u.Username
	}
	return n
}

func inputPeer(p peer) tg.InputPeerClass {
	switch p.Kind {
	case 'u':
		return &tg.InputPeerUser{UserID: p.ID, AccessHash: p.Hash}
	case 'g':
		return &tg.InputPeerChat{ChatID: p.ID}
	default:
		return &tg.InputPeerChannel{ChannelID: p.ID, AccessHash: p.Hash}
	}
}

// dialogsOf turns a dialogs response into the connector's, archived when it is the archive folder's.
func dialogsOf(ds []tg.DialogClass, msgs []tg.MessageClass, e entities, archived bool, now time.Time) []dialog {
	top := map[string]tg.MessageClass{}
	for _, m := range msgs {
		if p, ok := e.peerOf(peerOfMessage(m)); ok || p.ID != 0 {
			top[fmt.Sprintf("%s/%d", p.key(), m.GetID())] = m
		}
	}
	var out []dialog
	for _, dc := range ds {
		d, ok := dc.(*tg.Dialog)
		if !ok {
			continue // a folder entry
		}
		p, ok := e.peerOf(d.Peer)
		if !ok {
			continue
		}
		x := dialog{Peer: p, Name: e.nameOf(d.Peer), Top: d.TopMessage, Unread: d.UnreadCount, Mentions: d.UnreadMentionsCount,
			ReadMax: d.ReadInboxMaxID, Archived: archived || d.FolderID == 1, Muted: int64(d.NotifySettings.MuteUntil) > now.Unix(), Kind: "group",
			Members: e.membersOf(d.Peer)}
		x.Photo, x.PhotoDC = e.photoOf(d.Peer)
		switch pp := d.Peer.(type) {
		case *tg.PeerUser:
			x.Kind = "dm"
			if u := e.users[pp.UserID]; u != nil {
				x.Bot, x.Self, x.Username = u.Bot, u.Self, u.Username
			}
		case *tg.PeerChannel:
			if ch, ok := e.chats[pp.ChannelID].(*tg.Channel); ok {
				x.Broadcast, x.Username = ch.Broadcast && !ch.Megagroup, ch.Username
			}
		}
		if m, ok := top[fmt.Sprintf("%s/%d", p.key(), d.TopMessage)]; ok {
			mm := messageOf(m, e)
			x.TopAt, x.TopOut, x.TopText, x.TopFrom = mm.At, mm.Out, clip(mm.Text, 100), mm.From
		}
		out = append(out, x)
	}
	return out
}

func peerOfMessage(m tg.MessageClass) tg.PeerClass {
	switch m := m.(type) {
	case *tg.Message:
		return m.PeerID
	case *tg.MessageService:
		return m.PeerID
	}
	return nil
}

// messageOf is a message in words: a forward says from whom, media uses localized placeholders ([图片] for an image,
// [文件 <name> · <size>] for a file), and a group event is a line of its own.
func messageOf(mc tg.MessageClass, e entities) message {
	switch m := mc.(type) {
	case *tg.Message:
		out := message{ID: m.ID, At: time.Unix(int64(m.Date), 0), Out: m.Out, Mentioned: m.Mentioned, Type: "TEXT"}
		if m.FromID != nil {
			if p, _ := e.peerOf(m.FromID); p.ID != 0 {
				out.FromKey = p.key()
			}
			out.From = e.nameOf(m.FromID)
		} else if m.PeerID != nil { // a private chat's or a channel post's: the peer itself
			if p, _ := e.peerOf(m.PeerID); p.ID != 0 {
				out.FromKey = p.key()
			}
			out.From = e.nameOf(m.PeerID)
		}
		// In a forum topic every message names the topic's creation message; only one with a top id answers a message.
		// A reply to another chat's message can't be found in this one.
		if h, ok := m.ReplyTo.(*tg.MessageReplyHeader); ok && (!h.ForumTopic || h.ReplyToTopID != 0) {
			to, _ := e.peerOf(h.ReplyToPeerID)
			here, _ := e.peerOf(m.PeerID)
			if h.ReplyToPeerID == nil || to.key() == here.key() {
				out.ReplyTo = h.ReplyToMsgID
			}
		}
		var parts []string
		if f := m.FwdFrom; f.FromName != "" || f.FromID != nil {
			from := f.FromName
			if from == "" {
				from = e.nameOf(f.FromID)
			}
			if from == "" {
				from = "别处"
			}
			parts = append(parts, "转发自 "+from+"：")
		}
		if m.Message != "" {
			parts = append(parts, m.Message)
		}
		if kind, say := mediaOf(m.Media); say != "" {
			out.Type = kind
			parts = append(parts, say)
		}
		out.Text = strings.Join(parts, " ")
		return out
	case *tg.MessageService:
		out := message{ID: m.ID, At: time.Unix(int64(m.Date), 0), Out: m.Out, Type: "SYSTEM", Text: actionOf(m.Action, e)}
		if m.FromID != nil {
			out.From = e.nameOf(m.FromID)
		}
		return out
	}
	return message{ID: mc.GetID(), Type: "SYSTEM", Text: "[消息]"}
}

// mediaOf is a message's media as a placeholder; a link preview says nothing (the link is in the text).
func mediaOf(m tg.MessageMediaClass) (kind, say string) {
	switch m := m.(type) {
	case nil, *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
		return "", ""
	case *tg.MessageMediaPhoto:
		return "IMAGE", "[图片]"
	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return "FILE", "[文件]"
		}
		name := ""
		for _, a := range doc.Attributes {
			switch a := a.(type) {
			case *tg.DocumentAttributeSticker:
				return "STICKER", "[贴纸]"
			case *tg.DocumentAttributeAudio:
				if a.Voice {
					return "AUDIO", "[语音]"
				}
				return "AUDIO", "[音频]"
			case *tg.DocumentAttributeVideo:
				if a.RoundMessage {
					return "VIDEO", "[视频消息]"
				}
				return "VIDEO", "[视频]"
			case *tg.DocumentAttributeAnimated:
				return "IMAGE", "[动图]"
			case *tg.DocumentAttributeFilename:
				name = a.FileName
			}
		}
		if name != "" {
			return "FILE", fmt.Sprintf("[文件 %s · %s]", name, size(doc.Size))
		}
		return "FILE", "[文件]"
	case *tg.MessageMediaGeo, *tg.MessageMediaGeoLive, *tg.MessageMediaVenue:
		return "LOCATION", "[位置]"
	case *tg.MessageMediaContact:
		return "CONTACT", "[名片]"
	case *tg.MessageMediaPoll:
		return "POLL", "[投票]"
	}
	return "OTHER", "[消息]"
}

func size(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// actionOf is a group event in a line.
func actionOf(a tg.MessageActionClass, e entities) string {
	switch a := a.(type) {
	case *tg.MessageActionChatAddUser:
		var names []string
		for _, id := range a.Users {
			names = append(names, userName(e.users[id]))
		}
		return "[" + strings.Join(names, "、") + " 进群]"
	case *tg.MessageActionChatJoinedByLink, *tg.MessageActionChatJoinedByRequest:
		return "[有人进群]"
	case *tg.MessageActionChatDeleteUser:
		return "[" + userName(e.users[a.UserID]) + " 退群]"
	case *tg.MessageActionChatEditTitle:
		return "[群名改成「" + a.Title + "」]"
	case *tg.MessageActionPinMessage:
		return "[置顶了一条消息]"
	case *tg.MessageActionChatCreate, *tg.MessageActionChannelCreate:
		return "[建群]"
	}
	return "[群里的变化]"
}

// The gotd client's reads.

func (g *gotdClient) Dialogs(ctx context.Context, limit int) ([]dialog, error) {
	var out []dialog
	for _, folder := range []int{0, 1} {
		res, err := g.c.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: limit, FolderID: folder})
		if err != nil {
			return nil, err
		}
		var ds []tg.DialogClass
		var msgs []tg.MessageClass
		var e entities
		switch r := res.(type) {
		case *tg.MessagesDialogs:
			ds, msgs, e = r.Dialogs, r.Messages, entitiesOf(r.Users, r.Chats)
		case *tg.MessagesDialogsSlice:
			ds, msgs, e = r.Dialogs, r.Messages, entitiesOf(r.Users, r.Chats)
		default:
			continue
		}
		out = append(out, dialogsOf(ds, msgs, e, folder == 1, time.Now())...)
	}
	return out, nil
}

func (g *gotdClient) History(ctx context.Context, d dialog, minID int, since time.Time, max int) ([]message, error) {
	if minID > 0 {
		return g.historyAfter(ctx, d, minID, max)
	}
	var got []message
	offset := 0
	for len(got) < max {
		page := min(100, max-len(got))
		res, err := g.c.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: inputPeer(d.Peer), OffsetID: offset, Limit: page, MinID: minID})
		if err != nil {
			return nil, err
		}
		msgs, e := messagesOf(res)
		stop := len(msgs) < page
		for _, m := range msgs { // newest first
			mm := messageOf(m, e)
			if minID == 0 && mm.At.Before(since) {
				stop = true
				break
			}
			got = append(got, mm)
			offset = mm.ID
		}
		if stop || len(msgs) == 0 {
			break
		}
		// The next page waits its turn at the gate (client.go), like every request.
	}
	for i, j := 0, len(got)-1; i < j; i, j = i+1, j-1 { // oldest first
		got[i], got[j] = got[j], got[i]
	}
	return got, nil
}

// historyAfter reads up from minID, oldest first, at most max messages: the ones right after minID, so the next read
// goes on from the newest of them. Telegram pages newest first; add_offset -page turns a page to the page messages
// from offset_id up.
func (g *gotdClient) historyAfter(ctx context.Context, d dialog, minID int, max int) ([]message, error) {
	var got []message
	from := minID
	for len(got) < max {
		page := min(100, max-len(got))
		res, err := g.c.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: inputPeer(d.Peer), OffsetID: from + 1, AddOffset: -page, Limit: page, MinID: minID})
		if err != nil {
			return nil, err
		}
		msgs, e := messagesOf(res)
		next := from
		for i := len(msgs) - 1; i >= 0; i-- { // newest first: turn around
			if mm := messageOf(msgs[i], e); mm.ID > from && len(got) < max {
				got = append(got, mm)
				next = mm.ID
			}
		}
		if len(msgs) < page || next == from {
			break
		}
		from = next
		// The next page waits its turn at the gate (client.go), like every request.
	}
	return got, nil
}

// messagesOf is a messages response's messages and the entities they name.
func messagesOf(res tg.MessagesMessagesClass) ([]tg.MessageClass, entities) {
	switch r := res.(type) {
	case *tg.MessagesMessages:
		return r.Messages, entitiesOf(r.Users, r.Chats)
	case *tg.MessagesMessagesSlice:
		return r.Messages, entitiesOf(r.Users, r.Chats)
	case *tg.MessagesChannelMessages:
		return r.Messages, entitiesOf(r.Users, r.Chats)
	}
	return nil, entitiesOf(nil, nil)
}

func (g *gotdClient) Older(ctx context.Context, d dialog, before int, until time.Time, limit int) ([]message, error) {
	req := &tg.MessagesGetHistoryRequest{Peer: inputPeer(d.Peer), OffsetID: before, Limit: limit}
	if !until.IsZero() {
		req.OffsetDate = int(until.Unix())
	}
	res, err := g.c.API().MessagesGetHistory(ctx, req)
	if err != nil {
		return nil, err
	}
	msgs, e := messagesOf(res)
	out := make([]message, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- { // newest first: turn around
		out = append(out, messageOf(msgs[i], e))
	}
	return out, nil
}

func (g *gotdClient) Search(ctx context.Context, query string, in *dialog, since, until time.Time, limit int) ([]found, error) {
	minDate, maxDate := unixOr0(since), unixOr0(until)
	var res tg.MessagesMessagesClass
	var err error
	if in != nil {
		res, err = g.c.API().MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: inputPeer(in.Peer), Q: query, Filter: &tg.InputMessagesFilterEmpty{},
			MinDate: minDate, MaxDate: maxDate, Limit: limit})
	} else {
		res, err = g.c.API().MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{Q: query, Filter: &tg.InputMessagesFilterEmpty{},
			MinDate: minDate, MaxDate: maxDate, OffsetPeer: &tg.InputPeerEmpty{}, Limit: limit})
	}
	if err != nil {
		return nil, err
	}
	msgs, e := messagesOf(res)
	out := make([]found, 0, len(msgs))
	for _, m := range msgs {
		pc := peerOfMessage(m)
		p, _ := e.peerOf(pc)
		d := dialog{Peer: p, Name: e.nameOf(pc), Kind: "group", Members: e.membersOf(pc)}
		switch pp := pc.(type) {
		case *tg.PeerUser:
			d.Kind = "dm"
			if u := e.users[pp.UserID]; u != nil {
				d.Bot, d.Self, d.Username = u.Bot, u.Self, u.Username
			}
		case *tg.PeerChannel:
			if ch, ok := e.chats[pp.ChannelID].(*tg.Channel); ok {
				d.Broadcast, d.Username = ch.Broadcast && !ch.Megagroup, ch.Username
			}
		}
		out = append(out, found{Dialog: d, Message: messageOf(m, e)})
	}
	return out, nil
}

func (g *gotdClient) UnreadMentions(ctx context.Context, d dialog, limit int) ([]message, error) {
	res, err := g.c.API().MessagesGetUnreadMentions(ctx, &tg.MessagesGetUnreadMentionsRequest{Peer: inputPeer(d.Peer), Limit: limit})
	if err != nil {
		return nil, err
	}
	msgs, e := messagesOf(res)
	out := make([]message, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- { // newest first: turn around
		out = append(out, messageOf(msgs[i], e))
	}
	return out, nil
}

// unixOr0 is t as Telegram's dates, 0 (no bound) for a zero t.
func unixOr0(t time.Time) int {
	if t.IsZero() {
		return 0
	}
	return int(t.Unix())
}

func inputChannel(p peer) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: p.ID, AccessHash: p.Hash}
}

func (g *gotdClient) Messages(ctx context.Context, d dialog, ids []int) ([]message, error) {
	in := make([]tg.InputMessageClass, len(ids))
	for i, id := range ids {
		in[i] = &tg.InputMessageID{ID: id}
	}
	var res tg.MessagesMessagesClass
	var err error
	if d.Peer.Kind == 'c' {
		res, err = g.c.API().ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: inputChannel(d.Peer), ID: in})
	} else {
		res, err = g.c.API().MessagesGetMessages(ctx, in)
	}
	if err != nil {
		return nil, err
	}
	msgs, e := messagesOf(res)
	out := make([]message, 0, len(msgs))
	for _, m := range msgs {
		if inDialog(m, d) { // not empty, and not another chat's
			out = append(out, messageOf(m, e))
		}
	}
	return out, nil
}

// inDialog says m is in d. DMs and basic groups share one message id space per account, and messages.getMessages
// takes bare ids, so it answers an id from whichever chat holds it.
func inDialog(m tg.MessageClass, d dialog) bool {
	p, _ := entities{}.peerOf(peerOfMessage(m))
	return p.ID != 0 && p.key() == d.Peer.key()
}

func (g *gotdClient) Info(ctx context.Context, d dialog) (chatInfo, error) {
	switch d.Peer.Kind {
	case 'u':
		full, err := g.c.API().UsersGetFullUser(ctx, &tg.InputUser{UserID: d.Peer.ID, AccessHash: d.Peer.Hash})
		if err != nil {
			return chatInfo{}, err
		}
		return chatInfo{About: full.FullUser.About}, nil
	case 'g':
		full, err := g.c.API().MessagesGetFullChat(ctx, d.Peer.ID)
		if err != nil {
			return chatInfo{}, err
		}
		info := chatInfo{}
		if cf, ok := full.FullChat.(*tg.ChatFull); ok {
			info.About = cf.About
			if ps, ok := cf.Participants.(*tg.ChatParticipants); ok {
				info.Members = len(ps.Participants)
			}
		}
		return info, nil
	}
	full, err := g.c.API().ChannelsGetFullChannel(ctx, inputChannel(d.Peer))
	if err != nil {
		return chatInfo{}, err
	}
	info := chatInfo{}
	if cf, ok := full.FullChat.(*tg.ChannelFull); ok {
		info.About = cf.About
		info.Members, _ = cf.GetParticipantsCount()
	}
	return info, nil
}

func (g *gotdClient) Members(ctx context.Context, d dialog, limit int) ([]member, int, error) {
	var ids []int64
	var users []tg.UserClass
	count := 0
	switch d.Peer.Kind {
	case 'u':
		return nil, 0, errors.New("私聊没有成员列表")
	case 'g':
		full, err := g.c.API().MessagesGetFullChat(ctx, d.Peer.ID)
		if err != nil {
			return nil, 0, err
		}
		if cf, ok := full.FullChat.(*tg.ChatFull); ok {
			if ps, ok := cf.Participants.(*tg.ChatParticipants); ok {
				for _, p := range ps.Participants {
					ids = append(ids, p.GetUserID())
				}
			}
		}
		users, count = full.Users, len(ids)
	default:
		res, err := g.c.API().ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{Channel: inputChannel(d.Peer),
			Filter: &tg.ChannelParticipantsRecent{}, Limit: limit})
		if err != nil {
			return nil, 0, err
		}
		ps, ok := res.(*tg.ChannelsChannelParticipants)
		if !ok {
			return nil, 0, nil
		}
		for _, p := range ps.Participants {
			if u, ok := p.(interface{ GetUserID() int64 }); ok {
				ids = append(ids, u.GetUserID())
			}
		}
		users, count = ps.Users, ps.Count
	}
	e := entitiesOf(users, nil)
	out := make([]member, 0, min(len(ids), limit))
	for _, id := range ids {
		if len(out) == limit {
			break
		}
		m := member{Key: peer{Kind: 'u', ID: id}.key()}
		if u := e.users[id]; u != nil {
			m.Name, m.Username, m.Bot = userName(u), u.Username, u.Bot
		}
		out = append(out, m)
	}
	return out, count, nil
}

func (g *gotdClient) Send(ctx context.Context, d dialog, text string, replyTo int, random int64) (int, error) {
	req := &tg.MessagesSendMessageRequest{Peer: inputPeer(d.Peer), Message: text, RandomID: random}
	if replyTo != 0 {
		req.ReplyTo = &tg.InputReplyToMessage{ReplyToMsgID: replyTo}
	}
	up, err := g.c.API().MessagesSendMessage(ctx, req)
	if err != nil {
		return 0, err
	}
	return sentID(up, random), nil
}

// sentID is the id Telegram gave the message sent with random (0 when the answer does not say).
func sentID(up tg.UpdatesClass, random int64) int {
	var list []tg.UpdateClass
	switch u := up.(type) {
	case *tg.UpdateShortSentMessage:
		return u.ID
	case *tg.Updates:
		list = u.Updates
	case *tg.UpdatesCombined:
		list = u.Updates
	}
	for _, x := range list {
		if m, ok := x.(*tg.UpdateMessageID); ok && m.RandomID == random {
			return m.ID
		}
	}
	return 0
}

func (g *gotdClient) Forward(ctx context.Context, from dialog, ids []int, to dialog, random []int64) error {
	_, err := g.c.API().MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{FromPeer: inputPeer(from.Peer), ID: ids,
		RandomID: random, ToPeer: inputPeer(to.Peer)})
	return err
}

func (g *gotdClient) React(ctx context.Context, d dialog, id int, emoji string) error {
	_, err := g.c.API().MessagesSendReaction(ctx, &tg.MessagesSendReactionRequest{Peer: inputPeer(d.Peer), MsgID: id,
		Reaction: []tg.ReactionClass{&tg.ReactionEmoji{Emoticon: emoji}}})
	return err
}

func (g *gotdClient) MarkRead(ctx context.Context, d dialog, upTo int) error {
	if d.Peer.Kind == 'c' {
		_, err := g.c.API().ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: inputChannel(d.Peer), MaxID: upTo})
		return err
	}
	_, err := g.c.API().MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: inputPeer(d.Peer), MaxID: upTo})
	return err
}
