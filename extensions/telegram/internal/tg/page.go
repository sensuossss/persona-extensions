package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// The gotd side of the conversation page (thread.go): a page of messages as they are (media and senders' pictures
// with where to fetch them), and fetching a picture. Pure where it can be (tested on made-up objects).

// fileLoc is where a picture is: gotd's location, what it is, how big. A test's fake client names one by key.
type fileLoc struct {
	loc  tg.InputFileLocationClass
	mime string
	size int64
	key  string
	dc   int // the data center it is kept on, 0 when not known (dc.go: then the account's finds it)
}

// fullEdge is the longest side of a photo the page fetches: sharp on a phone's screen, a few hundred KB.
const fullEdge = 1280

// photoOf is a photo's size to fetch (the largest up to fullEdge; the smallest when all are larger) and its pixels.
func photoOf(p *tg.Photo) (*fileLoc, int, int) {
	best, bw, bh := "", 0, 0
	smallest, sw, sh := "", 0, 0
	for _, sz := range p.Sizes {
		var t string
		var w, h int
		switch s := sz.(type) {
		case *tg.PhotoSize:
			t, w, h = s.Type, s.W, s.H
		case *tg.PhotoSizeProgressive:
			t, w, h = s.Type, s.W, s.H
		default:
			continue // stripped and cached sizes are inline, not fetched
		}
		edge := max(w, h)
		if edge <= fullEdge && edge > max(bw, bh) {
			best, bw, bh = t, w, h
		}
		if smallest == "" || edge < max(sw, sh) {
			smallest, sw, sh = t, w, h
		}
	}
	if best == "" {
		best, bw, bh = smallest, sw, sh
	}
	if best == "" {
		return nil, 0, 0
	}
	return &fileLoc{loc: &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: best},
		mime: "image/jpeg", dc: p.DCID}, bw, bh
}

// docThumb is a document's largest thumbnail (a video's, an animated sticker's), nil without one.
func docThumb(d *tg.Document) *fileLoc {
	best, edge, mime := "", 0, "image/jpeg"
	for _, sz := range d.Thumbs {
		if s, ok := sz.(*tg.PhotoSize); ok && max(s.W, s.H) > edge {
			best, edge = s.Type, max(s.W, s.H)
		}
	}
	if best == "" {
		return nil
	}
	if strings.HasPrefix(d.MimeType, "application/x-tgsticker") || d.MimeType == "video/webm" || d.MimeType == "image/webp" {
		mime = "image/webp" // a sticker's thumbnail is WebP
	}
	return &fileLoc{loc: &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference, ThumbSize: best}, mime: mime,
		dc: d.DCID}
}

// pageMediaOf is a message's media for the page; nil for none (a link preview, whose link is in the text) and for
// what has no media of its own (a location, a contact, a poll: their placeholder is in the words).
func pageMediaOf(mc tg.MessageMediaClass) *pageMedia {
	switch m := mc.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := m.Photo.(*tg.Photo)
		if !ok {
			return &pageMedia{Kind: "image"}
		}
		loc, w, h := photoOf(p)
		return &pageMedia{Kind: "image", Mime: "image/jpeg", Width: w, Height: h, Full: loc}
	case *tg.MessageMediaDocument:
		d, ok := m.Document.(*tg.Document)
		if !ok {
			return &pageMedia{Kind: "file"}
		}
		out := &pageMedia{Kind: "file", Mime: d.MimeType, Size: d.Size, Thumb: docThumb(d)}
		for _, a := range d.Attributes {
			switch a := a.(type) {
			case *tg.DocumentAttributeFilename:
				out.Name = a.FileName
			case *tg.DocumentAttributeImageSize:
				out.Width, out.Height = a.W, a.H
			case *tg.DocumentAttributeSticker:
				out.Kind, out.Emoji = "sticker", a.Alt
			case *tg.DocumentAttributeAudio:
				out.Duration = a.Duration
				if out.Kind == "file" && a.Voice {
					out.Kind = "voice"
				}
			case *tg.DocumentAttributeVideo:
				out.Width, out.Height, out.Duration = a.W, a.H, int(a.Duration)
				if out.Kind == "file" {
					out.Kind = "video"
				}
			case *tg.DocumentAttributeAnimated:
				if out.Kind == "file" {
					out.Kind = "video"
				}
			}
		}
		if out.Kind == "sticker" && d.MimeType == "image/webp" && d.Size <= 1<<20 { // a static sticker: the sticker itself
			out.Full = &fileLoc{loc: &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference},
				mime: "image/webp", size: d.Size, dc: d.DCID}
		}
		return out
	}
	return nil
}

// pageMessageOf is a message as the page draws it.
func pageMessageOf(mc tg.MessageClass, e entities) pageMessage {
	out := pageMessage{message: messageOf(mc, e)}
	switch m := mc.(type) {
	case *tg.Message:
		from := m.FromID
		if from == nil {
			from = m.PeerID
		}
		out.FromPeer, _ = e.peerOf(from)
		out.FromPhoto, out.FromDC = e.photoOf(from)
		out.Edited = m.EditDate != 0 && !m.EditHide
		out.Media = pageMediaOf(m.Media)
		var parts []string
		if f := m.FwdFrom; f.FromName != "" || f.FromID != nil {
			who := f.FromName
			if who == "" {
				who = e.nameOf(f.FromID)
			}
			if who == "" {
				who = "别处"
			}
			parts = append(parts, "转发自 "+who+"：")
		}
		if m.Message != "" {
			parts = append(parts, m.Message)
		}
		if out.Media == nil { // a location, a contact, a poll: its placeholder
			if _, say := mediaOf(m.Media); say != "" {
				parts = append(parts, say)
			}
		}
		out.Caption = strings.Join(parts, " ")
	case *tg.MessageService:
		out.Event, out.Caption = true, out.Text
	default:
		out.Event, out.Caption = true, out.Text
	}
	return out
}

// photoOf is a peer's picture id in the entities (a person's, a group's), 0 without one, and the data center it is on.
func (e entities) photoOf(p tg.PeerClass) (id int64, dc int) {
	switch p := p.(type) {
	case *tg.PeerUser:
		if u := e.users[p.UserID]; u != nil {
			if ph, ok := u.Photo.(*tg.UserProfilePhoto); ok {
				return ph.PhotoID, ph.DCID
			}
		}
	case *tg.PeerChat:
		if c, ok := e.chats[p.ChatID].(*tg.Chat); ok {
			if ph, ok := c.Photo.(*tg.ChatPhoto); ok {
				return ph.PhotoID, ph.DCID
			}
		}
	case *tg.PeerChannel:
		if c, ok := e.chats[p.ChannelID].(*tg.Channel); ok {
			if ph, ok := c.Photo.(*tg.ChatPhoto); ok {
				return ph.PhotoID, ph.DCID
			}
		}
	}
	return 0, 0
}

// avatarLoc is where a peer's small picture is: no file reference needed, only the peer and the photo's id (and its
// data center, 0 when not known).
func avatarLoc(p peer, photo int64, dc int) *fileLoc {
	return &fileLoc{loc: &tg.InputPeerPhotoFileLocation{Peer: inputPeer(p), PhotoID: photo}, mime: "image/jpeg", dc: dc}
}

func (g *gotdClient) Page(ctx context.Context, d dialog, before, limit int) ([]pageMessage, map[string]peer, error) {
	res, err := g.c.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: inputPeer(d.Peer), OffsetID: before, Limit: limit})
	if err != nil {
		return nil, nil, err
	}
	msgs, e := messagesOf(res)
	out := make([]pageMessage, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- { // newest first: turn around
		if _, empty := msgs[i].(*tg.MessageEmpty); !empty {
			out = append(out, pageMessageOf(msgs[i], e))
		}
	}
	return out, e.peers(), nil
}

// peers are every user and chat the entities name, with their access hashes (for their pictures).
func (e entities) peers() map[string]peer {
	out := map[string]peer{}
	for id := range e.users {
		if p, ok := e.peerOf(&tg.PeerUser{UserID: id}); ok {
			out[p.key()] = p
		}
	}
	for id, c := range e.chats {
		switch c.(type) {
		case *tg.Chat:
			out[peer{Kind: 'g', ID: id}.key()] = peer{Kind: 'g', ID: id}
		case *tg.Channel:
			if p, ok := e.peerOf(&tg.PeerChannel{ChannelID: id}); ok {
				out[p.key()] = p
			}
		}
	}
	return out
}

func (g *gotdClient) Locate(ctx context.Context, d dialog, id int, thumb bool) (*fileLoc, error) {
	in := []tg.InputMessageClass{&tg.InputMessageID{ID: id}}
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
	msgs, _ := messagesOf(res)
	for _, mc := range msgs {
		if m, ok := mc.(*tg.Message); ok && m.ID == id && inDialog(m, d) {
			if md := pageMediaOf(m.Media); md != nil {
				if thumb && md.Thumb != nil {
					return md.Thumb, nil
				}
				if !thumb && md.Full != nil {
					return md.Full, nil
				}
			}
		}
	}
	return nil, errMediaGone
}

var errMediaGone = errors.New("这条消息里没有能取的图了")

// capped takes at most n bytes, then fails.
type capped struct {
	buf bytes.Buffer
	n   int
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.n {
		return 0, fmt.Errorf("more than %d bytes", c.n)
	}
	return c.buf.Write(p)
}

func (g *gotdClient) Fetch(ctx context.Context, f *fileLoc, most int) ([]byte, error) {
	home := g.c.Config().ThisDC
	traceOf(ctx).update(func(t *fetchTrace) { t.home, t.from = home, home })
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	// One part after another, each a request through the connector's gate (dc.go).
	return fetchFrom(ctx, f, home, g.c, g.pools, most)
}
