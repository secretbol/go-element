package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

const (
	me     = id.UserID("@me:hs")
	dmRoom = id.RoomID("!dm:hs")
	group  = id.RoomID("!group:hs")
	group2 = id.RoomID("!group2:hs")
)

type fakeHS struct {
	mu   sync.Mutex
	sent []string // "<method> <path>"
	body []map[string]any
}

func (f *fakeHS) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, r.Method+" "+r.URL.Path)
	var b map[string]any
	_ = json.NewDecoder(r.Body).Decode(&b)
	f.body = append(f.body, b)
}

func (f *fakeHS) handler() http.Handler {
	type dn = map[string]any
	members := map[string]map[string]dn{
		string(dmRoom): {string(me): {"display_name": "Me"}, "@a.asadollahi:hs": {"display_name": "Ali"}},
		string(group):  {string(me): {}, "@x:hs": {}, "@y:hs": {}},
		string(group2): {string(me): {}, "@x:hs": {}, "@y:hs": {}},
	}
	names := map[string]string{string(group): "SRE Fast Response", string(group2): "SRE Fast Response Archive"}
	mux := http.NewServeMux()
	mux.HandleFunc("/_matrix/client/v3/joined_rooms", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"joined_rooms": []string{string(dmRoom), string(group), string(group2)}})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/{room}/joined_members", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"joined": members[r.PathValue("room")]})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/{room}/state/m.room.name/", func(w http.ResponseWriter, r *http.Request) {
		n, ok := names[r.PathValue("room")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"no name"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"name": n})
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/{room}/state/m.room.canonical_alias/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"no alias"}`))
	})
	mux.HandleFunc("/_matrix/client/v3/rooms/{room}/messages", func(w http.ResponseWriter, _ *http.Request) {
		// newest first, as the spec's backward direction returns them
		_ = json.NewEncoder(w).Encode(map[string]any{"chunk": []map[string]any{
			{"event_id": "$3", "type": "m.room.encrypted", "sender": "@a.asadollahi:hs", "origin_server_ts": 3000,
				"content": map[string]any{"algorithm": "m.megolm.v1.aes-sha2", "ciphertext": "x", "session_id": "s", "sender_key": "k"}},
			{"event_id": "$2", "type": "m.room.message", "sender": "@a.asadollahi:hs", "origin_server_ts": 2000,
				"content": map[string]any{"msgtype": "m.text", "body": "second"}},
			{"event_id": "$1", "type": "m.room.message", "sender": string(me), "origin_server_ts": 1000,
				"content": map[string]any{"msgtype": "m.text", "body": "first"}},
		}})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		f.record(r)
		_, _ = w.Write([]byte(`{"event_id":"$sent"}`))
	})
	return mux
}

type failDec struct{}

func (failDec) Decrypt(context.Context, *event.Event) (*event.Event, error) {
	return nil, errors.New("no session")
}

func newTestClient(t *testing.T, dec decrypter) (*Client, *fakeHS) {
	t.Helper()
	f := &fakeHS{}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	cli, err := mautrix.NewClient(srv.URL, me, "tok")
	if err != nil {
		t.Fatal(err)
	}
	return newClient(cli, dec), f
}

func TestRoomsNamesDMsAndUnreadOrder(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t, nil)
	c.unread[group] = 4

	rooms, err := c.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 3 || rooms[0].ID != group || rooms[0].Unread != 4 {
		t.Fatalf("unread room must come first: %+v", rooms)
	}
	for _, r := range rooms {
		if r.ID == dmRoom && (r.Name != "Ali" || r.DMWith != "@a.asadollahi:hs" || r.Members != 2) {
			t.Fatalf("DM room: %+v", r)
		}
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t, nil)
	ctx := context.Background()

	for spec, want := range map[string]id.RoomID{
		"!raw:hs":           "!raw:hs",
		"a.asadollahi":      dmRoom,
		"@a.asadollahi:hs":  dmRoom,
		"sre fast response": group, // exact beats substring
		"archive":           group2,
	} {
		got, err := c.Resolve(ctx, spec)
		if err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", spec, got, err, want)
		}
	}
	if _, err := c.Resolve(ctx, "SRE"); err == nil || !strings.Contains(err.Error(), string(group2)) {
		t.Errorf("ambiguous name must list candidates, got %v", err)
	}
	if _, err := c.Resolve(ctx, "nothing-like-this"); err == nil {
		t.Error("unknown name must fail")
	}
}

func TestHistoryOrderAndUndecryptable(t *testing.T) {
	t.Parallel()
	c, _ := newTestClient(t, failDec{})

	msgs, err := c.History(context.Background(), dmRoom, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[0].Body != "first" || msgs[1].Body != "second" {
		t.Fatalf("must be oldest first: %+v", msgs)
	}
	if !msgs[2].Encrypted || !msgs[2].Undecryptable || !strings.Contains(msgs[2].Body, "no session") {
		t.Fatalf("encrypted event the device cannot read: %+v", msgs[2])
	}
}

func TestSendAndMarkRead(t *testing.T) {
	t.Parallel()
	c, f := newTestClient(t, nil)
	ctx := context.Background()

	ev, err := c.Send(ctx, dmRoom, "hello **world**")
	if err != nil || ev != "$sent" {
		t.Fatalf("Send = %q, %v", ev, err)
	}
	last, err := c.MarkRead(ctx, dmRoom)
	if err != nil || last != "$3" {
		t.Fatalf("MarkRead = %q, %v", last, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(f.sent[0], "PUT /_matrix/client/v3/rooms/!dm:hs/send/m.room.message/") {
		t.Fatalf("send path: %v", f.sent)
	}
	if f.body[0]["body"] != "hello **world**" || !strings.Contains(fmt.Sprint(f.body[0]["formatted_body"]), "<strong>world</strong>") {
		t.Fatalf("body must carry plain Markdown and rendered HTML: %v", f.body[0])
	}
	if len(f.sent) < 2 || !strings.HasSuffix(f.sent[1], "/receipt/m.read/$3") {
		t.Fatalf("mark read must post an m.read receipt for the latest event: %v", f.sent)
	}
}
