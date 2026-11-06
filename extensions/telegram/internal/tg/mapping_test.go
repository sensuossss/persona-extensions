package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// Telegram's objects, made up: two people (one a bot), a supergroup, a broadcast channel, a basic group.
func exampleEntities() entities {
	group := &tg.Channel{ID: 2, AccessHash: 22, Title: "示例群", Megagroup: true}
	group.SetParticipantsCount(128) // said only sometimes: a flag
	return entitiesOf(
		[]tg.UserClass{
			&tg.User{ID: 1, AccessHash: 11, FirstName: "示例", LastName: "用户", Username: "example", Photo: &tg.UserProfilePhoto{PhotoID: 501, DCID: 4}},
			&tg.User{ID: 3, AccessHash: 33, FirstName: "示例机器人", Bot: true},
		},
		[]tg.ChatClass{
			group,
			&tg.Channel{ID: 5, AccessHash: 55, Title: "示例频道", Broadcast: true},
			&tg.Chat{ID: 6, Title: "示例小群", ParticipantsCount: 5},
		})
}

func TestDialogsOf(t *testing.T) {
	e := exampleEntities()
	now := time.Unix(1_800_000_000, 0)
	ds := []tg.DialogClass{
		&tg.Dialog{Peer: &tg.PeerUser{UserID: 1}, TopMessage: 10, UnreadCount: 2, ReadInboxMaxID: 8},
		&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 2}, TopMessage: 20, UnreadMentionsCount: 1,
			NotifySettings: tg.PeerNotifySettings{MuteUntil: int(now.Unix()) + 3600}},
		&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 5}, TopMessage: 30},
		&tg.Dialog{Peer: &tg.PeerChat{ChatID: 6}, TopMessage: 40, FolderID: 1},
		&tg.Dialog{Peer: &tg.PeerUser{UserID: 3}, TopMessage: 50},
	}
	msgs := []tg.MessageClass{
		&tg.Message{ID: 10, PeerID: &tg.PeerUser{UserID: 1}, FromID: &tg.PeerUser{UserID: 1}, Date: int(now.Unix()), Message: "示例消息"},
		&tg.Message{ID: 20, PeerID: &tg.PeerChannel{ChannelID: 2}, Out: true, Date: int(now.Unix()), Message: "我的示例消息"},
	}
	got := dialogsOf(ds, msgs, e, false, now)
	if len(got) != 5 {
		t.Fatalf("%d dialogs", len(got))
	}
	dm, group, channel, small, bot := got[0], got[1], got[2], got[3], got[4]
	if dm.Kind != "dm" || dm.Peer != (peer{'u', 1, 11}) || dm.Name != "示例 用户" || dm.Unread != 2 || dm.ReadMax != 8 ||
		dm.TopText != "示例消息" || dm.TopFrom != "示例 用户" || dm.Username != "example" || !dm.kept() || dm.Photo != 501 || dm.PhotoDC != 4 {
		t.Fatalf("dm %+v", dm)
	}
	if group.Kind != "group" || group.Peer != (peer{'c', 2, 22}) || !group.Muted || group.Mentions != 1 || !group.TopOut || group.Broadcast || !group.kept() ||
		group.Members != 128 {
		t.Fatalf("supergroup %+v", group)
	}
	if !channel.Broadcast || channel.kept() {
		t.Fatalf("a broadcast channel is kept: %+v", channel)
	}
	if small.Peer.Kind != 'g' || !small.Archived || small.Name != "示例小群" || small.Members != 5 || channel.Members != 0 || dm.Members != 0 {
		t.Fatalf("basic group %+v", small)
	}
	if !bot.Bot || bot.kept() {
		t.Fatalf("a bot is kept: %+v", bot)
	}
}

func TestMessageOf(t *testing.T) {
	e := exampleEntities()
	at := time.Unix(1_800_000_000, 0)
	for _, c := range []struct {
		name, text, kind string
		m                tg.MessageClass
	}{
		{"a forwarded photo", "转发自 示例来源： 示例消息 [图片]", "IMAGE", &tg.Message{ID: 1, Date: int(at.Unix()), Message: "示例消息",
			FwdFrom: tg.MessageFwdHeader{FromName: "示例来源"}, Media: &tg.MessageMediaPhoto{}}},
		{"a file", "[文件 example.pdf · 2.0 MB]", "FILE", &tg.Message{ID: 2, Date: int(at.Unix()),
			Media: &tg.MessageMediaDocument{Document: &tg.Document{Size: 2 << 20, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "example.pdf"}}}}}},
		{"a voice message", "[语音]", "AUDIO", &tg.Message{ID: 3, Date: int(at.Unix()),
			Media: &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}}}}},
		{"a sticker", "[贴纸]", "STICKER", &tg.Message{ID: 4, Date: int(at.Unix()),
			Media: &tg.MessageMediaDocument{Document: &tg.Document{Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}}}}},
		{"a link preview says nothing", "https://example.com", "TEXT", &tg.Message{ID: 5, Date: int(at.Unix()), Message: "https://example.com",
			Media: &tg.MessageMediaWebPage{}}},
		{"a group's new name", "[群名改成「新的示例群」]", "SYSTEM", &tg.MessageService{ID: 6, Date: int(at.Unix()),
			Action: &tg.MessageActionChatEditTitle{Title: "新的示例群"}}},
		{"someone joins", "[示例 用户 进群]", "SYSTEM", &tg.MessageService{ID: 7, Date: int(at.Unix()),
			Action: &tg.MessageActionChatAddUser{Users: []int64{1}}}},
	} {
		m := messageOf(c.m, e)
		if m.Text != c.text || m.Type != c.kind || !m.At.Equal(at) {
			t.Errorf("%s: %q %s %v", c.name, m.Text, m.Type, m.At)
		}
	}
	reply := messageOf(&tg.Message{ID: 8, Date: int(at.Unix()), Message: "示例回复", Mentioned: true, FromID: &tg.PeerUser{UserID: 1},
		PeerID: &tg.PeerChannel{ChannelID: 2}, ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 7}}, e)
	if reply.ReplyTo != 7 || !reply.Mentioned || reply.FromKey != "u1" || reply.From != "示例 用户" {
		t.Fatalf("reply %+v", reply)
	}
	post := messageOf(&tg.Message{ID: 9, Date: int(at.Unix()), Message: "频道的示例消息", PeerID: &tg.PeerChannel{ChannelID: 5}}, e)
	if post.FromKey != "c5" || post.From != "示例频道" {
		t.Fatalf("a post without a sender %+v", post)
	}
}

// In a forum every message sent in a topic carries reply_to {forum_topic, reply_to_msg_id: <topic id>}, the topic id
// being its creation message's; only reply_to_top_id makes it a real reply. A reply to another chat's message isn't
// one to this chat's message of that id.
func TestReplyToInForumsAndOtherChats(t *testing.T) {
	e := exampleEntities()
	at := time.Unix(1_800_000_000, 0)
	const topic = 100
	in := func(id int, h *tg.MessageReplyHeader) *tg.Message {
		return &tg.Message{ID: id, Date: int(at.Unix()), Message: "话题里的示例消息", FromID: &tg.PeerUser{UserID: 1},
			PeerID: &tg.PeerChannel{ChannelID: 2}, ReplyTo: h}
	}
	for _, c := range []struct {
		name string
		h    *tg.MessageReplyHeader
		want int
	}{
		{"a plain topic message", &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: topic}, 0},
		{"a reply inside a topic", &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 102, ReplyToTopID: topic}, 102},
		{"a reply to another chat", &tg.MessageReplyHeader{ReplyToMsgID: 7, ReplyToPeerID: &tg.PeerChannel{ChannelID: 5}}, 0},
		{"a reply naming its own chat", &tg.MessageReplyHeader{ReplyToMsgID: 7, ReplyToPeerID: &tg.PeerChannel{ChannelID: 2}}, 7},
	} {
		if got := messageOf(in(101, c.h), e).ReplyTo; got != c.want {
			t.Errorf("%s: replies to %d, want %d", c.name, got, c.want)
		}
	}
	create := &tg.MessageService{ID: topic, Date: int(at.Unix()), PeerID: &tg.PeerChannel{ChannelID: 2},
		Action: &tg.MessageActionTopicCreate{Title: "示例话题"}}
	plain := in(101, &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: topic})
	if got := sourceMessages([]message{messageOf(create, e), messageOf(plain, e)})[1].Text; got != "话题里的示例消息" {
		t.Errorf("a plain topic message archived as %q", got)
	}
	if p := pageMessageOf(plain, e); p.ReplyTo != 0 {
		t.Errorf("the page quotes message %d over a plain topic message", p.ReplyTo)
	}
}

// A chat found by search keeps its @username, so a write can name it that way after tg search as after tg chats.
func TestSearchKeepsUsernames(t *testing.T) {
	user := &tg.User{ID: 42, AccessHash: 4242, FirstName: "示例", Username: "example_user"}
	group := &tg.Channel{ID: 2, AccessHash: 22, Title: "示例群", Megagroup: true, Username: "example_group"}
	now := int(time.Now().Unix())
	dm := &tg.Message{ID: 5, PeerID: &tg.PeerUser{UserID: 42}, FromID: &tg.PeerUser{UserID: 42}, Date: now, Message: "示例发布说明"}
	post := &tg.Message{ID: 6, PeerID: &tg.PeerChannel{ChannelID: 2}, FromID: &tg.PeerUser{UserID: 42}, Date: now, Message: "示例发布说明"}
	gc := offlineGotd(func(input bin.Encoder, output bin.Decoder) error {
		switch r := input.(type) {
		case *tg.MessagesGetDialogsRequest:
			res := &tg.MessagesDialogs{Users: []tg.UserClass{user}}
			if r.FolderID == 0 {
				res.Dialogs = []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 42}, TopMessage: 5}}
				res.Messages = []tg.MessageClass{dm}
			}
			output.(*tg.MessagesDialogsBox).Dialogs = res
		case *tg.MessagesSearchGlobalRequest:
			output.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{dm, post},
				Users: []tg.UserClass{user}, Chats: []tg.ChatClass{group}}
		default:
			return fmt.Errorf("unexpected %T", input)
		}
		return nil
	})
	ctx := context.Background()
	found, err := gc.Search(ctx, "示例", nil, time.Time{}, time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Dialog.Username != "example_user" || found[1].Dialog.Username != "example_group" {
		t.Fatalf("found %+v", found)
	}

	c := newConn(&memSecrets{}, nil)
	c.cl = gc
	send := json.RawMessage(`{"chat":"@example_user","text":"你好"}`)
	if _, err := c.Call(ctx, "telegram_list_chats", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Describe(ctx, "telegram_send", send); err != nil {
		t.Fatalf("after tg chats: %v", err)
	}
	if _, err := c.Call(ctx, "telegram_search_messages", json.RawMessage(`{"query":"示例"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Describe(ctx, "telegram_send", send); err != nil {
		t.Fatalf("after tg search: %v", err)
	}
	if _, _, err := c.Describe(ctx, "telegram_send", json.RawMessage(`{"chat":"@example_group","text":"你好"}`)); err != nil {
		t.Fatalf("a group found only by tg search: %v", err)
	}
}

// DMs and basic groups share one message id space per account, and messages.getMessages takes bare ids: id 500
// asked for "in" the DM u7 comes back from wherever it is. A message from another chat is not the named chat's,
// for tg get-message as for a media ref (#214).
func TestMessagesStayInTheNamedChat(t *testing.T) {
	now := int(time.Now().Unix())
	friend := &tg.User{ID: 7, AccessHash: 77, FirstName: "示例好友"}
	other := &tg.User{ID: 8, AccessHash: 88, FirstName: "示例群友"}
	group := &tg.Chat{ID: 9, Title: "示例群", ParticipantsCount: 3}
	photo := &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 3, DCID: 2, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 800, H: 600}}}}
	dm := &tg.Message{ID: 400, PeerID: &tg.PeerUser{UserID: 7}, FromID: &tg.PeerUser{UserID: 7}, Date: now, Message: "示例私聊"}
	inGroup := &tg.Message{ID: 500, PeerID: &tg.PeerChat{ChatID: 9}, FromID: &tg.PeerUser{UserID: 8}, Date: now,
		Message: "示例群消息", Media: photo}
	byID := map[int]tg.MessageClass{400: dm, 500: inGroup}
	gc := offlineGotd(func(input bin.Encoder, output bin.Decoder) error {
		switch r := input.(type) {
		case *tg.MessagesGetDialogsRequest:
			res := &tg.MessagesDialogs{Users: []tg.UserClass{friend, other}, Chats: []tg.ChatClass{group}}
			if r.FolderID == 0 {
				res.Dialogs = []tg.DialogClass{&tg.Dialog{Peer: &tg.PeerUser{UserID: 7}, TopMessage: 400},
					&tg.Dialog{Peer: &tg.PeerChat{ChatID: 9}, TopMessage: 500}}
				res.Messages = []tg.MessageClass{dm, inGroup}
			}
			output.(*tg.MessagesDialogsBox).Dialogs = res
		case *tg.MessagesGetMessagesRequest: // like Telegram: each id wherever it is among the account's DMs and groups
			res := &tg.MessagesMessages{Users: []tg.UserClass{friend, other}, Chats: []tg.ChatClass{group}}
			for _, in := range r.ID {
				res.Messages = append(res.Messages, byID[in.(*tg.InputMessageID).ID])
			}
			output.(*tg.MessagesMessagesBox).Messages = res
		default:
			return fmt.Errorf("unexpected %T", input)
		}
		return nil
	})
	ctx := context.Background()
	c := newConn(&memSecrets{}, nil)
	if out, err := c.cliCall(ctx, gc, "telegram_get_message", json.RawMessage(`{"chat":"u7","id":500}`)); err == nil {
		t.Fatalf("u7 #500 answered a message from g9: %s", out)
	} else if !strings.Contains(err.Error(), "没有消息 500") {
		t.Fatalf("u7 #500: %v", err)
	}
	if out, err := c.cliCall(ctx, gc, "telegram_get_message", json.RawMessage(`{"chat":"g9","id":500}`)); err != nil ||
		!strings.Contains(out, "示例群消息") {
		t.Fatalf("g9 #500: %s, %v", out, err)
	}
	if out, err := c.cliCall(ctx, gc, "telegram_get_message", json.RawMessage(`{"chat":"u7","id":400}`)); err != nil ||
		!strings.Contains(out, "示例私聊") {
		t.Fatalf("u7 #400: %s, %v", out, err)
	}

	u7 := dialog{Peer: peer{Kind: 'u', ID: 7, Hash: 77}, Kind: "dm"}
	if f, err := gc.Locate(ctx, u7, 500, false); err != errMediaGone {
		t.Fatalf("a media ref u7/500 found %+v, %v", f, err)
	}
	if _, err := gc.Locate(ctx, dialog{Peer: peer{Kind: 'g', ID: 9}, Kind: "group"}, 500, false); err != nil {
		t.Fatalf("g9/500: %v", err)
	}
}
