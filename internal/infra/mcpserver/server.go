// Package mcpserver exposes the Matrix client as MCP tools.
package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/secretbol/go-element/internal/infra/matrix"
)

// Server is the MCP server for one logged-in Matrix client.
type Server struct{ srv *mcp.Server }

type empty struct{}

type roomArg struct {
	Room string `json:"room" jsonschema:"room id, alias, user (the DM with them), or room name"`
}

type roomsArgs struct {
	Unread bool   `json:"unread,omitempty" jsonschema:"only rooms with unread messages"`
	Query  string `json:"query,omitempty" jsonschema:"keep rooms whose name or DM user contains this"`
}

type historyArgs struct {
	Room  string `json:"room" jsonschema:"room id, alias, user (the DM with them), or room name"`
	Limit int    `json:"limit,omitempty" jsonschema:"newest N messages, default 20, max 200"`
}

type sendArgs struct {
	Room string `json:"room" jsonschema:"room id, alias, user (the DM with them), or room name"`
	Text string `json:"text" jsonschema:"message in Markdown"`
	Yes  bool   `json:"yes,omitempty" jsonschema:"false (default) is a dry run that only shows the resolved room; true really sends"`
}

type identity struct {
	UserID   string `json:"user_id"`
	DeviceID string `json:"device_id"`
}

type roomsOut struct {
	Rooms []matrix.Room `json:"rooms"`
}

type membersOut struct {
	Room    string          `json:"room"`
	Members []matrix.Member `json:"members"`
}

type historyOut struct {
	Room     string           `json:"room"`
	Messages []matrix.Message `json:"messages"`
}

type sendOut struct {
	Room    string `json:"room"`
	Name    string `json:"name"`
	Members int    `json:"members"`
	Sent    bool   `json:"sent"`
	EventID string `json:"event_id,omitempty"`
	Note    string `json:"note,omitempty"`
}

type readOut struct {
	Room    string `json:"room"`
	EventID string `json:"event_id"`
}

// New registers every tool on a new server.
func New(c *matrix.Client, l *matrix.Listener, version string) *Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "go-element", Version: version}, &mcp.ServerOptions{
		Instructions: "Matrix/Element as the logged-in user. Writes are dry runs until send is called with yes=true; " +
			"send only what the user asked for, to the room they named, and check the member count first (a DM has 2).",
	})

	mcp.AddTool(srv, &mcp.Tool{Name: "whoami", Description: "Show which account and device this server acts as."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, identity, error) {
			return nil, identity{UserID: string(c.UserID()), DeviceID: string(c.DeviceID())}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "rooms", Description: "List joined rooms with member and unread counts, unread first."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a roomsArgs) (*mcp.CallToolResult, roomsOut, error) {
			rooms, err := c.Rooms(ctx)
			if err != nil {
				return nil, roomsOut{}, fmt.Errorf("rooms: %w", err)
			}
			rooms = filterRooms(rooms, a)
			return nil, roomsOut{Rooms: rooms}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "members", Description: "List the joined members of a room."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a roomArg) (*mcp.CallToolResult, membersOut, error) {
			rid, err := c.Resolve(ctx, a.Room)
			if err != nil {
				return nil, membersOut{}, fmt.Errorf("resolve: %w", err)
			}
			m, err := c.Members(ctx, rid)
			if err != nil {
				return nil, membersOut{}, fmt.Errorf("members: %w", err)
			}
			return nil, membersOut{Room: string(rid), Members: m}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "history", Description: "Read a room's newest messages, oldest first, decrypting what this device can."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a historyArgs) (*mcp.CallToolResult, historyOut, error) {
			limit := min(max(a.Limit, 1), 200)
			if a.Limit == 0 {
				limit = 20
			}
			rid, err := c.Resolve(ctx, a.Room)
			if err != nil {
				return nil, historyOut{}, fmt.Errorf("resolve: %w", err)
			}
			msgs, err := c.History(ctx, rid, limit)
			if err != nil {
				return nil, historyOut{}, fmt.Errorf("history: %w", err)
			}
			return nil, historyOut{Room: string(rid), Messages: msgs}, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "send", Description: "Send a Markdown message as the user. Dry run unless yes=true."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a sendArgs) (*mcp.CallToolResult, sendOut, error) {
			if a.Text == "" {
				return nil, sendOut{}, errors.New("text is empty")
			}
			rid, err := c.Resolve(ctx, a.Room)
			if err != nil {
				return nil, sendOut{}, fmt.Errorf("resolve: %w", err)
			}
			rooms, err := c.Rooms(ctx)
			if err != nil {
				return nil, sendOut{}, fmt.Errorf("rooms: %w", err)
			}
			out := sendOut{Room: string(rid)}
			for _, r := range rooms {
				if r.ID == rid {
					out.Name, out.Members = r.Name, r.Members
				}
			}
			if !a.Yes {
				out.Note = "dry run: nothing sent. Call again with yes=true to send."
				return nil, out, nil
			}
			ev, err := c.Send(ctx, rid, a.Text)
			if err != nil {
				return nil, sendOut{}, fmt.Errorf("send: %w", err)
			}
			out.Sent, out.EventID = true, string(ev)
			return nil, out, nil
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "mark_read", Description: "Mark a room read up to its latest event."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a roomArg) (*mcp.CallToolResult, readOut, error) {
			rid, err := c.Resolve(ctx, a.Room)
			if err != nil {
				return nil, readOut{}, fmt.Errorf("resolve: %w", err)
			}
			ev, err := c.MarkRead(ctx, rid)
			if err != nil {
				return nil, readOut{}, fmt.Errorf("mark read: %w", err)
			}
			return nil, readOut{Room: string(rid), EventID: string(ev)}, nil
		})

	if l != nil {
		addVerificationTools(srv, c, l)
	}

	return &Server{srv: srv}
}

type matchArgs struct {
	Match bool `json:"match" jsonschema:"true only if the user confirmed the emojis are identical on both screens; false cancels"`
}

type verifyOut struct {
	matrix.VerificationState

	Verified bool   `json:"verified"`
	Note     string `json:"note,omitempty"`
}

func addVerificationTools(srv *mcp.Server, c *matrix.Client, l *matrix.Listener) {
	snapshot := func(ctx context.Context, note string) (verifyOut, error) {
		st, err := c.VerificationStatus(ctx)
		if err != nil {
			return verifyOut{}, fmt.Errorf("status: %w", err)
		}
		return verifyOut{VerificationState: l.State(), Verified: st.Verified, Note: note}, nil
	}

	mcp.AddTool(srv, &mcp.Tool{Name: "verification", Description: "Show this device's verification state. A request made in Element (Settings > Sessions > go-element > Verify session) is accepted automatically; when phase is comparing, show the user the emojis and ask whether they match before calling verification_confirm."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ empty) (*mcp.CallToolResult, verifyOut, error) {
			out, err := snapshot(ctx, "")
			return nil, out, err
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "verification_confirm", Description: "Answer a pending emoji comparison. Call it only after the user said the emojis match; never answer for them."},
		func(ctx context.Context, _ *mcp.CallToolRequest, a matchArgs) (*mcp.CallToolResult, verifyOut, error) {
			if err := l.Answer(a.Match); err != nil {
				return nil, verifyOut{}, fmt.Errorf("answer: %w", err)
			}
			out, err := snapshot(ctx, "answer sent; call verification to see the outcome")
			return nil, out, err
		})
}

// Run serves MCP over stdio until the context ends.
func (s *Server) Run(ctx context.Context) error {
	if err := s.srv.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("stdio transport: %w", err)
	}
	return nil
}
