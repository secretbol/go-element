package matrix

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

const firstSyncTimeout = 2 * time.Minute

// decrypter decrypts one Megolm event; *cryptohelper.CryptoHelper implements it.
type decrypter interface {
	Decrypt(ctx context.Context, evt *event.Event) (*event.Event, error)
}

// Client is a logged-in, end-to-end-encrypted Matrix client that syncs in the background.
type Client struct {
	cli    *mautrix.Client
	crypto *cryptohelper.CryptoHelper
	dec    decrypter
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	names map[id.RoomID]string
}

// Open loads the stored session, starts syncing and waits for the first sync,
// which also collects the room keys waiting for this device.
func Open(ctx context.Context, dir string) (*Client, error) {
	return OpenWith(ctx, dir, nil)
}

// OpenWith is Open, but calls setup after the crypto helper is ready and before
// syncing starts, so event handlers can be registered without racing the syncer.
func OpenWith(ctx context.Context, dir string, setup func(*Client) error) (*Client, error) {
	s, err := LoadSession(dir)
	if err != nil {
		return nil, err
	}
	cli, err := mautrix.NewClient(s.Homeserver, s.UserID, s.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	cli.DeviceID = s.DeviceID

	c := &Client{
		cli:   cli,
		names: map[id.RoomID]string{},
		done:  make(chan struct{}),
	}
	first := make(chan struct{})
	var once sync.Once
	syncer, ok := cli.Syncer.(*mautrix.DefaultSyncer)
	if !ok {
		return nil, errors.New("unexpected syncer type")
	}
	syncer.OnSync(func(_ context.Context, _ *mautrix.RespSync, _ string) bool {
		once.Do(func() { close(first) })
		return true
	})

	helper, err := cryptohelper.NewCryptoHelper(cli, []byte(s.PickleKey), filepath.Join(dir, "crypto.db"))
	if err != nil {
		return nil, fmt.Errorf("crypto helper: %w", err)
	}
	if err := helper.Init(ctx); err != nil {
		return nil, fmt.Errorf("crypto init: %w", err)
	}
	cli.Crypto = helper
	c.crypto = helper
	c.dec = helper

	if setup != nil {
		if err := setup(c); err != nil {
			_ = helper.Close()
			return nil, fmt.Errorf("setup: %w", err)
		}
	}

	syncCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.cancel = cancel
	go func() {
		defer close(c.done)
		_ = cli.SyncWithContext(syncCtx) // returns on cancel; sync errors retry inside
	}()

	select {
	case <-first:
	case <-time.After(firstSyncTimeout):
		c.Close()
		return nil, errors.New("first sync timed out")
	case <-ctx.Done():
		c.Close()
		return nil, fmt.Errorf("waiting for first sync: %w", ctx.Err())
	}
	return c, nil
}

// Close stops syncing and closes the crypto store.
func (c *Client) Close() {
	c.cancel()
	c.cli.StopSync()
	<-c.done
	if c.crypto != nil {
		_ = c.crypto.Close()
	}
}

// UserID is the logged-in user.
func (c *Client) UserID() id.UserID { return c.cli.UserID }

// DeviceID is this tool's device.
func (c *Client) DeviceID() id.DeviceID { return c.cli.DeviceID }

// Room is one joined room.
type Room struct {
	ID      id.RoomID `json:"id"`
	Name    string    `json:"name"`
	Members int       `json:"members"`
	Unread  int       `json:"unread"`
	// DMWith is set for a two-member room: the other user.
	DMWith id.UserID `json:"dm_with,omitempty"`
}

// Member is one joined member.
type Member struct {
	ID          id.UserID `json:"id"`
	DisplayName string    `json:"display_name"`
}

// Message is one room event, decrypted when possible.
type Message struct {
	EventID   id.EventID `json:"event_id"`
	Sender    id.UserID  `json:"sender"`
	Time      time.Time  `json:"time"`
	Body      string     `json:"body"`
	Encrypted bool       `json:"encrypted,omitempty"`
	// Undecryptable means this device never received the room key for the event.
	Undecryptable bool `json:"undecryptable,omitempty"`
}

// Rooms lists joined rooms, rooms with unread messages first.
func (c *Client) Rooms(ctx context.Context) ([]Room, error) {
	joined, err := c.cli.JoinedRooms(ctx)
	if err != nil {
		return nil, fmt.Errorf("joined rooms: %w", err)
	}
	unread := c.unreadCounts(ctx)
	rooms := make([]Room, 0, len(joined.JoinedRooms))
	for _, rid := range joined.JoinedRooms {
		members, err := c.Members(ctx, rid)
		if err != nil {
			continue // a room we cannot read is skipped, not fatal
		}
		r := Room{ID: rid, Members: len(members)}
		if len(members) == 2 {
			for _, m := range members {
				if m.ID != c.cli.UserID {
					r.DMWith = m.ID
				}
			}
		}
		r.Name = c.roomName(ctx, rid, members)
		r.Unread = unread[rid]
		rooms = append(rooms, r)
	}
	slices.SortStableFunc(rooms, func(a, b Room) int { return b.Unread - a.Unread })
	return rooms, nil
}

func (c *Client) roomName(ctx context.Context, rid id.RoomID, members []Member) string {
	c.mu.Lock()
	cached, ok := c.names[rid]
	c.mu.Unlock()
	if ok {
		return cached
	}
	var name event.RoomNameEventContent
	var alias event.CanonicalAliasEventContent
	switch {
	case c.cli.StateEvent(ctx, rid, event.StateRoomName, "", &name) == nil && name.Name != "":
		cached = name.Name
	case c.cli.StateEvent(ctx, rid, event.StateCanonicalAlias, "", &alias) == nil && alias.Alias != "":
		cached = string(alias.Alias)
	default:
		var others []string
		for _, m := range members {
			if m.ID != c.cli.UserID {
				others = append(others, cmpName(m))
			}
		}
		cached = strings.Join(others, ", ")
	}
	c.mu.Lock()
	c.names[rid] = cached
	c.mu.Unlock()
	return cached
}

func cmpName(m Member) string {
	if m.DisplayName != "" {
		return m.DisplayName
	}
	return string(m.ID)
}

// Members lists the joined members of a room.
func (c *Client) Members(ctx context.Context, rid id.RoomID) ([]Member, error) {
	resp, err := c.cli.JoinedMembers(ctx, rid)
	if err != nil {
		return nil, fmt.Errorf("joined members of %s: %w", rid, err)
	}
	out := make([]Member, 0, len(resp.Joined))
	for uid, m := range resp.Joined {
		out = append(out, Member{ID: uid, DisplayName: m.DisplayName})
	}
	slices.SortFunc(out, func(a, b Member) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out, nil
}

// Resolve turns a room id, an alias, a user (the two-member DM with them) or a
// room name (exact, then unique substring) into a room id. An ambiguous name
// returns an error that lists the candidates.
func (c *Client) Resolve(ctx context.Context, spec string) (id.RoomID, error) {
	spec = strings.TrimSpace(spec)
	if strings.HasPrefix(spec, "!") {
		return id.RoomID(spec), nil
	}
	if strings.HasPrefix(spec, "#") {
		resp, err := c.cli.ResolveAlias(ctx, id.RoomAlias(spec))
		if err != nil {
			return "", fmt.Errorf("resolving alias %s: %w", spec, err)
		}
		return resp.RoomID, nil
	}
	rooms, err := c.Rooms(ctx)
	if err != nil {
		return "", err
	}
	if dms := dmsWith(rooms, spec); len(dms) > 0 {
		return pickOne(spec, dms)
	}
	var exact, partial []Room
	for _, r := range rooms {
		switch {
		case strings.EqualFold(r.Name, spec):
			exact = append(exact, r)
		case strings.Contains(strings.ToLower(r.Name), strings.ToLower(spec)):
			partial = append(partial, r)
		}
	}
	for _, set := range [][]Room{exact, partial} {
		if len(set) > 0 {
			return pickOne(spec, set)
		}
	}
	return "", fmt.Errorf("no room matches %q", spec)
}

// dmsWith returns the two-member rooms whose other side is spec: a full user id or a localpart.
func dmsWith(rooms []Room, spec string) []Room {
	want := strings.TrimPrefix(spec, "@")
	var out []Room
	for _, r := range rooms {
		if r.DMWith == "" {
			continue
		}
		local, _, _ := strings.Cut(strings.TrimPrefix(string(r.DMWith), "@"), ":")
		if string(r.DMWith) == spec || local == want {
			out = append(out, r)
		}
	}
	return out
}

func pickOne(spec string, rooms []Room) (id.RoomID, error) {
	if len(rooms) == 1 {
		return rooms[0].ID, nil
	}
	return "", ambiguous(spec, rooms)
}

func ambiguous(spec string, rooms []Room) error {
	var b strings.Builder
	for _, r := range rooms {
		fmt.Fprintf(&b, "\n  %s  %q (%d members)", r.ID, r.Name, r.Members)
	}
	return fmt.Errorf("%q matches several rooms, pass the id of one:%s", spec, b.String())
}

// History returns the newest `limit` messages of a room, oldest first.
func (c *Client) History(ctx context.Context, rid id.RoomID, limit int) ([]Message, error) {
	resp, err := c.cli.Messages(ctx, rid, "", "", mautrix.DirectionBackward, nil, limit)
	if err != nil {
		return nil, fmt.Errorf("messages of %s: %w", rid, err)
	}
	out := make([]Message, 0, len(resp.Chunk))
	for _, evt := range slices.Backward(resp.Chunk) {
		if m, ok := c.message(ctx, evt); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (c *Client) message(ctx context.Context, evt *event.Event) (Message, bool) {
	m := Message{EventID: evt.ID, Sender: evt.Sender, Time: time.UnixMilli(evt.Timestamp)}
	if err := evt.Content.ParseRaw(evt.Type); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
		return m, false
	}
	if evt.Type == event.EventEncrypted {
		m.Encrypted = true
		if c.dec == nil {
			m.Undecryptable = true
			m.Body = "[undecryptable: no crypto]"
			return m, true
		}
		dec, err := c.dec.Decrypt(ctx, evt)
		if err != nil {
			m.Undecryptable = true
			m.Body = fmt.Sprintf("[undecryptable: %v]", err)
			return m, true
		}
		evt = dec
	}
	if evt.Type != event.EventMessage {
		return m, false
	}
	msg := evt.Content.AsMessage()
	if msg == nil {
		return m, false
	}
	m.Body = msg.Body
	if msg.NewContent != nil && msg.RelatesTo != nil && msg.RelatesTo.Type == event.RelReplace {
		m.Body = fmt.Sprintf("[edit of %s] %s", msg.RelatesTo.EventID, msg.NewContent.Body)
	}
	return m, true
}

// ErrCannotEncrypt means the room is encrypted but this client has no way to encrypt for it.
var ErrCannotEncrypt = errors.New("room is encrypted but encryption is unavailable; refusing to send plaintext")

// Send posts a Markdown message (also sent as HTML). In an encrypted room the
// message is encrypted, and the call fails rather than ever sending plaintext.
//
// The room state is read from the server first: the background sync resumes from
// a saved position, so the client's state store may never have seen the room's
// m.room.encryption event or its members, and would treat the room as plaintext.
func (c *Client) Send(ctx context.Context, rid id.RoomID, markdown string) (id.EventID, error) {
	encrypted, err := c.loadRoomState(ctx, rid)
	if err != nil {
		return "", err
	}
	content := format.RenderMarkdown(markdown, true, false)
	if !encrypted {
		resp, err := c.cli.SendMessageEvent(ctx, rid, event.EventMessage, &content)
		if err != nil {
			return "", fmt.Errorf("sending to %s: %w", rid, err)
		}
		return resp.EventID, nil
	}
	if c.crypto == nil {
		return "", ErrCannotEncrypt
	}
	enc, err := c.crypto.Encrypt(ctx, rid, event.EventMessage, &content)
	if err != nil {
		return "", fmt.Errorf("encrypting for %s: %w", rid, err)
	}
	resp, err := c.cli.SendMessageEvent(ctx, rid, event.EventEncrypted, enc)
	if err != nil {
		return "", fmt.Errorf("sending to %s: %w", rid, err)
	}
	return resp.EventID, nil
}

// loadRoomState copies the room's current state (encryption, members, power levels)
// into the client's state store and reports whether the room is encrypted.
func (c *Client) loadRoomState(ctx context.Context, rid id.RoomID) (bool, error) {
	state, err := c.cli.StateAsArray(ctx, rid)
	if err != nil {
		return false, fmt.Errorf("room state of %s: %w", rid, err)
	}
	encrypted := false
	for _, evt := range state {
		if err := evt.Content.ParseRaw(evt.Type); err != nil && !errors.Is(err, event.ErrContentAlreadyParsed) {
			continue
		}
		if evt.Type == event.StateEncryption {
			encrypted = true
		}
		mautrix.UpdateStateStore(ctx, c.cli.StateStore, evt)
	}
	if c.cli.StateStore != nil {
		if err := c.cli.StateStore.MarkMembersFetched(ctx, rid); err != nil {
			return false, fmt.Errorf("recording members of %s: %w", rid, err)
		}
	}
	return encrypted, nil
}

// MarkRead marks a room read up to its latest event and returns that event id.
func (c *Client) MarkRead(ctx context.Context, rid id.RoomID) (id.EventID, error) {
	resp, err := c.cli.Messages(ctx, rid, "", "", mautrix.DirectionBackward, nil, 1)
	if err != nil {
		return "", fmt.Errorf("latest event of %s: %w", rid, err)
	}
	if len(resp.Chunk) == 0 {
		return "", errors.New("room has no events")
	}
	last := resp.Chunk[0].ID
	if err := c.cli.MarkRead(ctx, rid, last); err != nil {
		return "", fmt.Errorf("marking %s read: %w", rid, err)
	}
	return last, nil
}

// newClient builds a Client on an already-configured mautrix client, without
// crypto or background sync. It exists for tests against a fake homeserver.
func newClient(cli *mautrix.Client, dec decrypter) *Client {
	return &Client{cli: cli, dec: dec, names: map[id.RoomID]string{}}
}

// countsFilter asks for one event per room and no state: the server leaves out rooms with an empty
// timeline, and only the per-room counters are wanted.
const countsFilter = `{"presence":{"types":[]},"account_data":{"types":[]},` +
	`"room":{"timeline":{"limit":1},"state":{"types":[]},"ephemeral":{"types":[]},"account_data":{"types":[]}}}`

// unreadCounts returns the server's notification count for every joined room.
// The background sync resumes from a saved position, so it only sees rooms that
// changed since the last run; a fresh initial sync with an empty filter is the
// way to get every room's counter. On error it returns no counts.
func (c *Client) unreadCounts(ctx context.Context) map[id.RoomID]int {
	out := map[id.RoomID]int{}
	resp, err := c.cli.SyncRequest(ctx, 0, "", countsFilter, false, event.PresenceOffline)
	if err != nil {
		return out
	}
	for rid, j := range resp.Rooms.Join {
		if j.UnreadNotifications != nil {
			out[rid] = j.UnreadNotifications.NotificationCount
		}
	}
	return out
}
