package mcpserver

import (
	"strings"

	"github.com/secretbol/go-element/internal/infra/matrix"
)

func filterRooms(rooms []matrix.Room, a roomsArgs) []matrix.Room {
	out := rooms[:0:0]
	q := strings.ToLower(a.Query)
	for _, r := range rooms {
		if a.Unread && r.Unread == 0 {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Name+" "+string(r.DMWith)), q) {
			continue
		}
		out = append(out, r)
	}
	return out
}
