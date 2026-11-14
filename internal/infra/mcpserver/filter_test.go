package mcpserver

import (
	"testing"

	"github.com/secretbol/go-element/internal/infra/matrix"
)

func TestFilterRooms(t *testing.T) {
	t.Parallel()

	rooms := []matrix.Room{
		{ID: "!a:x", Name: "SRE Fast Response", Unread: 3},
		{ID: "!b:x", Name: "Ali", DMWith: "@a.asadollahi:x"},
	}

	if got := filterRooms(rooms, roomsArgs{Unread: true}); len(got) != 1 || got[0].ID != "!a:x" {
		t.Fatalf("unread filter: %+v", got)
	}
	if got := filterRooms(rooms, roomsArgs{Query: "asadollahi"}); len(got) != 1 || got[0].ID != "!b:x" {
		t.Fatalf("query on DM user: %+v", got)
	}
	if got := filterRooms(rooms, roomsArgs{}); len(got) != 2 {
		t.Fatalf("no filter: %+v", got)
	}
}

// TestNewRegistersTools fails if any tool's input/output schema cannot be built,
// which the SDK reports by panicking at registration.
func TestNewRegistersTools(t *testing.T) {
	t.Parallel()

	if New(nil, nil, "test") == nil {
		t.Fatal("New returned nil")
	}
}
