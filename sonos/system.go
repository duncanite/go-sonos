package sonos

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The errors of finding a room.
var (
	// ErrNoRoom: no speaker by that name; the error lists the rooms there
	// are.
	ErrNoRoom = errors.New("no room by that name")
	// ErrSeveralRooms: more than one speaker carries the name.
	ErrSeveralRooms = errors.New("several rooms by that name")
)

// Player is one speaker a person sees: a room. The second speaker of a
// stereo pair, a surround, a sub or a bridge is not one.
type Player struct {
	// UUID is the speaker's permanent id, "RINCON_...".
	UUID string
	// Name is the room's name.
	Name string
	// Host is the speaker's address, with its port.
	Host string
}

// Group is the speakers that play together. Transport and group volume go
// to its coordinator.
type Group struct {
	// ID changes each time the group does.
	ID          string
	Coordinator Player
	// Members is the rooms in the group, the coordinator's among them.
	Members []Player
}

// System is a household as one speaker described it at one moment. It
// does not follow changes: after a Join or a Leave, or when a speaker has
// been regrouped elsewhere, Discover again.
type System struct {
	client *http.Client
	// Groups is every group that has a room in it.
	Groups []Group
}

// The zone group state, as the topology service gives it: an XML document
// inside the SOAP answer. Older software gives the ZoneGroups element as
// the root, newer wraps it in ZoneGroupState.
type (
	zoneGroupState struct {
		Wrapped []zoneGroup `xml:"ZoneGroups>ZoneGroup"`
		Bare    []zoneGroup `xml:"ZoneGroup"`
	}
	zoneGroup struct {
		Coordinator string       `xml:"Coordinator,attr"`
		ID          string       `xml:"ID,attr"`
		Members     []zoneMember `xml:"ZoneGroupMember"`
	}
	zoneMember struct {
		UUID      string `xml:"UUID,attr"`
		Location  string `xml:"Location,attr"`
		ZoneName  string `xml:"ZoneName,attr"`
		Invisible string `xml:"Invisible,attr"`
	}
)

// Discover asks the speaker at seed, a host with or without a port, for
// the household: any speaker knows all of it. client makes every request,
// then and later.
func Discover(ctx context.Context, client *http.Client, seed string) (*System, error) {
	host, err := address(seed)
	if err != nil {
		return nil, err
	}

	answer, err := call(ctx, client, host, topologyType, topologyPath, "GetZoneGroupState")
	if err != nil {
		return nil, err
	}

	var state zoneGroupState
	if err = xml.Unmarshal([]byte(answer["ZoneGroupState"]), &state); err != nil {
		return nil, fmt.Errorf("%w: the zone group state: %w", ErrAnswer, err)
	}

	system := &System{client: client}

	for _, zone := range append(state.Wrapped, state.Bare...) {
		group, err := grouped(zone)
		if err != nil {
			return nil, err
		}

		// A bridge or a sub alone: nothing a person plays to.
		if len(group.Members) > 0 {
			system.Groups = append(system.Groups, group)
		}
	}

	return system, nil
}

// grouped is one group of the zone group state: its rooms, and its
// coordinator, which is one of its speakers and may be no room.
func grouped(zone zoneGroup) (Group, error) {
	group := Group{ID: zone.ID}

	for _, member := range zone.Members {
		location, err := url.Parse(member.Location)
		if err != nil || location.Host == "" {
			return Group{}, fmt.Errorf("%w: the location %q of %s", ErrAnswer, member.Location, member.UUID)
		}

		player := Player{UUID: member.UUID, Name: member.ZoneName, Host: location.Host}

		if member.UUID == zone.Coordinator {
			group.Coordinator = player
		}

		if member.Invisible != "1" {
			group.Members = append(group.Members, player)
		}
	}

	if len(group.Members) > 0 && group.Coordinator.UUID == "" {
		return Group{}, fmt.Errorf("%w: the group %s names no coordinator among its members", ErrAnswer, zone.ID)
	}

	return group, nil
}

// Room finds the player named name, whatever its case, and the group it
// is in.
func (s *System) Room(name string) (Player, Group, error) {
	type place struct {
		player Player
		group  Group
	}

	var (
		found []place
		names []string
	)

	for _, group := range s.Groups {
		for _, member := range group.Members {
			names = append(names, member.Name)

			if strings.EqualFold(member.Name, name) {
				found = append(found, place{member, group})
			}
		}
	}

	switch len(found) {
	case 0:
		return Player{}, Group{}, fmt.Errorf("%w: %q; there are %s", ErrNoRoom, name, quoted(names))
	case 1:
		return found[0].player, found[0].group, nil
	default:
		return Player{}, Group{}, fmt.Errorf("%w: %q is %d speakers", ErrSeveralRooms, name, len(found))
	}
}

// quoted is names for a message: each quoted, since a room's name is its
// owner's text.
func quoted(names []string) string {
	each := make([]string, 0, len(names))
	for _, name := range names {
		each = append(each, fmt.Sprintf("%q", name))
	}

	return strings.Join(each, ", ")
}
