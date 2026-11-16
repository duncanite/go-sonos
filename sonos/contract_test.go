package sonos_test

// The contract, from the community's documentation of the speakers'
// services (https://sonos.svrooij.io/services/), generated from the
// speakers' own descriptions, each rule under the service it is on.
//
// Every service: SOAP over HTTP POST to the service's control URL on port
// 1400, the action named in the SOAPAction header as
// "<service type>#<action>"; a refusal is a UPnP fault with an error code.
//
// ZoneGroupTopology, /ZoneGroupTopology/Control: GetZoneGroupState takes
// nothing and gives ZoneGroupState, an XML document: a ZoneGroup per group
// with its Coordinator and ID, and a ZoneGroupMember per speaker with
// UUID, Location, ZoneName, and Invisible="1" for one that is no room.
// The same document from any speaker.
//
// AVTransport, /MediaRenderer/AVTransport/Control, InstanceID always 0:
//   - Play takes Speed "1"; Pause, Stop, Next, Previous take nothing more.
//   - GetTransportInfo gives CurrentTransportState: STOPPED, PLAYING,
//     PAUSED_PLAYBACK, TRANSITIONING. "Send to non-coordinator always
//     returns PLAYING."
//   - GetPositionInfo gives TrackDuration, TrackMetaData (embedded XML),
//     TrackURI, RelTime.
//   - SetAVTransportURI takes CurrentURI and CurrentURIMetaData: "If set
//     to another player RINCON, the player is grouped with that one."
//   - BecomeCoordinatorOfStandaloneGroup: "Leave the current group and
//     revert to a single player."
//
// RenderingControl, /MediaRenderer/RenderingControl/Control: GetVolume and
// GetMute take Channel "Master" and give CurrentVolume, 0 to 100, and
// CurrentMute; SetVolume takes DesiredVolume, SetMute DesiredMute, 1 or 0.
//
// A speaker with the UPnP setting off, from Home Assistant's page
// (https://www.home-assistant.io/integrations/sonos/) and not from the
// community's documentation: "403 Client Error: Forbidden", "This error
// means UPnP is not properly enabled on your Sonos system."
//
// GroupRenderingControl, /MediaRenderer/GroupRenderingControl/Control,
// "should be send to coordinator only": GetGroupVolume gives CurrentVolume;
// SetGroupVolume takes DesiredVolume and moves the players "proportionally
// based on last snapshot"; SnapshotGroupVolume takes that snapshot.

import (
	"bytes"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duncanite/go-sonos/sonos"
	"github.com/duncanite/go-sonos/sonos/sonostest"
)

// household is three rooms: Kitchen alone, Den coordinating Porch.
func household(t *testing.T) (*sonostest.Household, *sonos.System) {
	t.Helper()

	house := sonostest.New("Kitchen", "Den", "Porch")
	t.Cleanup(house.Close)

	house.Speakers[2].Group = house.Speakers[1].UUID

	return house, discovered(t, house)
}

func discovered(t *testing.T, house *sonostest.Household) *sonos.System {
	t.Helper()

	system, err := sonos.Discover(t.Context(), house.Speakers[0].Client(), house.Speakers[0].Host())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	return system
}

func room(t *testing.T, system *sonos.System, name string) (sonos.Player, sonos.Group) {
	t.Helper()

	player, group, err := system.Room(name)
	if err != nil {
		t.Fatalf("Room %q: %v", name, err)
	}

	return player, group
}

// TestDiscover: one speaker gives the whole household, its groups with
// their coordinators and rooms; a speaker that is no room is in no list;
// a group of none is left out; the older shape of the answer reads the
// same; a room is found whatever the case of its name.
func TestDiscover(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen", "Den", "Porch", "Den", "BRIDGE")
	t.Cleanup(house.Close)

	kitchen, den, porch, pair, bridge := house.Speakers[0], house.Speakers[1], house.Speakers[2],
		house.Speakers[3], house.Speakers[4]
	porch.Group, pair.Group = den.UUID, den.UUID
	pair.Invisible, bridge.Invisible = true, true

	for _, bare := range []bool{false, true} {
		house.Bare = bare

		system := discovered(t, house)
		if len(system.Groups) != 2 {
			t.Fatalf("bare %t: Discover found %+v", bare, system.Groups)
		}

		alone, together := system.Groups[0], system.Groups[1]
		if alone.Coordinator.UUID != kitchen.UUID || len(alone.Members) != 1 ||
			alone.Members[0] != (sonos.Player{UUID: kitchen.UUID, Name: "Kitchen", Host: kitchen.Host()}) {
			t.Errorf("bare %t: the kitchen's group reads %+v", bare, alone)
		}

		if together.Coordinator.Host != den.Host() || together.ID != den.UUID+":1" || len(together.Members) != 2 ||
			together.Members[0].Name != "Den" || together.Members[1].Name != "Porch" {
			t.Errorf("bare %t: the den's group reads %+v", bare, together)
		}

		player, group, err := system.Room("porch")
		if err != nil || player.UUID != porch.UUID || group.Coordinator.UUID != den.UUID {
			t.Errorf("bare %t: Room porch = %+v in %+v, %v", bare, player, group, err)
		}
	}
}

// TestRooms: a name no room has is ErrNoRoom, with the rooms there are; a
// name two rooms have is ErrSeveralRooms.
func TestRooms(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen", "Den", "Den")
	t.Cleanup(house.Close)

	system := discovered(t, house)

	_, _, err := system.Room("Garage")
	if !errors.Is(err, sonos.ErrNoRoom) || !strings.Contains(err.Error(), `"Kitchen", "Den", "Den"`) {
		t.Errorf("Room of a name no room has = %v, want ErrNoRoom with the rooms", err)
	}

	if _, _, err = system.Room("den"); !errors.Is(err, sonos.ErrSeveralRooms) {
		t.Errorf("Room of a name two rooms have = %v, want ErrSeveralRooms", err)
	}
}

// TestTransport: play, pause, stop, next and previous go to the group's
// coordinator, whichever room named the group, and to no other speaker.
func TestTransport(t *testing.T) {
	t.Parallel()

	house, system := household(t)
	den, porch := house.Speakers[1], house.Speakers[2]

	// The group, reached through the room that does not coordinate it.
	_, group := room(t, system, "Porch")

	for _, step := range []struct {
		name string
		do   func() error
		want string
	}{
		{"Play", func() error { return system.Play(t.Context(), group) }, "PLAYING"},
		{"Pause", func() error { return system.Pause(t.Context(), group) }, "PAUSED_PLAYBACK"},
		{"Next", func() error { return system.Next(t.Context(), group) }, "PAUSED_PLAYBACK"},
		{"Previous", func() error { return system.Previous(t.Context(), group) }, "PAUSED_PLAYBACK"},
		{"Stop", func() error { return system.Stop(t.Context(), group) }, "STOPPED"},
	} {
		if err := step.do(); err != nil || house.State[den.UUID] != step.want {
			t.Fatalf("%s = %v, the den is %s, want %s", step.name, err, house.State[den.UUID], step.want)
		}
	}

	want := []string{
		"AVTransport.Play", "AVTransport.Pause", "AVTransport.Next", "AVTransport.Previous", "AVTransport.Stop",
	}
	if got := den.Received(); !slices.Equal(got, want) {
		t.Errorf("the coordinator received %v, want %v", got, want)
	}

	if got := porch.Received(); len(got) != 0 {
		t.Errorf("the other member received %v", got)
	}
}

// TestStatus: the state and the track of what a group plays, read from
// its coordinator; a source with no track and no times reads as none.
func TestStatus(t *testing.T) {
	t.Parallel()

	house, system := household(t)
	den := house.Speakers[1]
	_, group := room(t, system, "Porch")

	house.State[den.UUID] = "PLAYING"
	house.URI[den.UUID] = "x-sonos-http:track.mp4"
	house.Position[den.UUID] = "0:01:05"
	house.Duration[den.UUID] = "1:02:03"
	house.Track[den.UUID] = `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" ` +
		`xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"><item id="-1" parentID="-1">` +
		`<dc:title>Blue &amp; Green</dc:title><dc:creator>A Band</dc:creator>` +
		`<upnp:album>An Album</upnp:album></item></DIDL-Lite>`

	status, err := system.Status(t.Context(), group)

	want := sonos.Status{
		State: sonos.Playing, Title: "Blue & Green", Artist: "A Band", Album: "An Album",
		URI: "x-sonos-http:track.mp4", Position: 65 * time.Second, Duration: time.Hour + 2*time.Minute + 3*time.Second,
	}
	if err != nil || status != want {
		t.Fatalf("Status = %+v, %v\nwant %+v", status, err, want)
	}

	// A radio stream: no track document, and a time that is none.
	house.Track[den.UUID], house.Position[den.UUID], house.Duration[den.UUID] = "NOT_IMPLEMENTED", "NOT_IMPLEMENTED", ""
	house.State[den.UUID] = "TRANSITIONING"

	status, err = system.Status(t.Context(), group)
	if err != nil || status != (sonos.Status{State: sonos.Transitioning, URI: "x-sonos-http:track.mp4"}) {
		t.Fatalf("Status of a stream = %+v, %v", status, err)
	}
}

// TestVolume: a player's volume and mute are its own, read and set on that
// speaker; a group's volume goes to the coordinator, after a snapshot; a
// volume outside 0 to 100 is refused before anything is sent.
func TestVolume(t *testing.T) {
	t.Parallel()

	house, system := household(t)
	den, porch := house.Speakers[1], house.Speakers[2]
	player, group := room(t, system, "Porch")

	if err := system.SetVolume(t.Context(), player, 35); err != nil || porch.Volume != 35 || den.Volume != 20 {
		t.Fatalf("SetVolume = %v; porch at %d, den at %d", err, porch.Volume, den.Volume)
	}

	if level, err := system.Volume(t.Context(), player); err != nil || level != 35 {
		t.Fatalf("Volume = %d, %v", level, err)
	}

	if err := system.SetMute(t.Context(), player, true); err != nil || !porch.Muted || den.Muted {
		t.Fatalf("SetMute = %v; porch muted %t, den muted %t", err, porch.Muted, den.Muted)
	}

	if muted, err := system.Muted(t.Context(), player); err != nil || !muted {
		t.Fatalf("Muted = %t, %v", muted, err)
	}

	if err := system.SetMute(t.Context(), player, false); err != nil || porch.Muted {
		t.Fatalf("SetMute off = %v; porch muted %t", err, porch.Muted)
	}

	before := len(den.Received())

	if err := system.SetGroupVolume(t.Context(), group, 50); err != nil || den.Volume != 50 || porch.Volume != 50 {
		t.Fatalf("SetGroupVolume = %v; den at %d, porch at %d", err, den.Volume, porch.Volume)
	}

	if got := den.Received()[before:]; !slices.Equal(got,
		[]string{"GroupRenderingControl.SnapshotGroupVolume", "GroupRenderingControl.SetGroupVolume"}) {
		t.Errorf("the group's volume went out as %v", got)
	}

	if level, err := system.GroupVolume(t.Context(), group); err != nil || level != 50 {
		t.Fatalf("GroupVolume = %d, %v", level, err)
	}

	sent := len(den.Received()) + len(porch.Received())

	for _, level := range []int{-1, 101} {
		if err := system.SetVolume(t.Context(), player, level); !errors.Is(err, sonos.ErrArgument) {
			t.Errorf("SetVolume to %d = %v, want ErrArgument", level, err)
		}

		if err := system.SetGroupVolume(t.Context(), group, level); !errors.Is(err, sonos.ErrArgument) {
			t.Errorf("SetGroupVolume to %d = %v, want ErrArgument", level, err)
		}
	}

	for _, level := range []int{0, 100} {
		if err := system.SetVolume(t.Context(), player, level); err != nil || porch.Volume != level {
			t.Errorf("SetVolume to %d = %v, porch at %d", level, err, porch.Volume)
		}
	}

	if got := len(den.Received()) + len(porch.Received()); got != sent+2 {
		t.Errorf("the refused volumes sent %d requests", got-sent-2)
	}
}

// TestGrouping: Join moves a player into a group, by its coordinator's
// id; Leave takes it out; a group's own coordinator cannot join it.
func TestGrouping(t *testing.T) {
	t.Parallel()

	house, system := household(t)
	kitchen, den := house.Speakers[0], house.Speakers[1]

	player, _ := room(t, system, "Kitchen")
	_, group := room(t, system, "Porch")

	if err := system.Join(t.Context(), player, group); err != nil || kitchen.Group != den.UUID {
		t.Fatalf("Join = %v; the kitchen is in %s", err, kitchen.Group)
	}

	if joined := discovered(t, house); len(joined.Groups) != 1 || len(joined.Groups[0].Members) != 3 {
		t.Fatalf("after the join the household reads %+v", joined.Groups)
	}

	if err := system.Leave(t.Context(), player); err != nil || kitchen.Group != kitchen.UUID {
		t.Fatalf("Leave = %v; the kitchen is in %s", err, kitchen.Group)
	}

	coordinator, _ := room(t, system, "Den")
	if err := system.Join(t.Context(), coordinator, group); !errors.Is(err, sonos.ErrArgument) {
		t.Errorf("Join of a coordinator to its own group = %v, want ErrArgument", err)
	}
}

// TestRefusals: a UPnP fault is ErrRefused with its code; a 403 is
// ErrForbidden; an answer that is not the documented SOAP is ErrAnswer; a
// host that is none is ErrArgument.
func TestRefusals(t *testing.T) {
	t.Parallel()

	house, system := household(t)
	porch := house.Speakers[2]

	// A group as it no longer is: the porch taken for its coordinator.
	player, _ := room(t, system, "Porch")
	stale := sonos.Group{Coordinator: player}

	if err := system.Play(t.Context(), stale); !errors.Is(err, sonos.ErrRefused) ||
		!strings.Contains(err.Error(), "UPnP error 800") {
		t.Errorf("Play on a speaker that does not coordinate = %v, want ErrRefused with the code", err)
	}

	for name, raw := range map[string]string{
		"not XML":        `{"not": "xml"}`,
		"no SOAP body":   `<Envelope><Header/></Envelope>`,
		"an empty reply": ``,
		"a volume that is no number": `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
			`<u:GetVolumeResponse><CurrentVolume>loud</CurrentVolume></u:GetVolumeResponse></s:Body></s:Envelope>`,
	} {
		house.Raw = []byte(raw)

		if _, err := system.Volume(t.Context(), player); !errors.Is(err, sonos.ErrAnswer) {
			t.Errorf("Volume answered %s = %v, want ErrAnswer", name, err)
		}
	}

	house.Raw = []byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>` +
		`<u:GetZoneGroupStateResponse><ZoneGroupState>&lt;ZoneGroups&gt;&lt;ZoneGroup Coordinator="A" ID="A:1"&gt;` +
		`&lt;ZoneGroupMember UUID="B" Location="http://127.0.0.1:1400/x" ZoneName="Room"/&gt;` +
		`&lt;/ZoneGroup&gt;&lt;/ZoneGroups&gt;</ZoneGroupState></u:GetZoneGroupStateResponse></s:Body></s:Envelope>`)

	if _, err := sonos.Discover(t.Context(), porch.Client(), porch.Host()); !errors.Is(err, sonos.ErrAnswer) {
		t.Errorf("Discover of a group whose coordinator is no member = %v, want ErrAnswer", err)
	}

	house.Raw = bytes.Repeat([]byte(" "), 4<<20+1)

	if _, err := system.Volume(t.Context(), player); !errors.Is(err, sonos.ErrAnswer) {
		t.Errorf("Volume answered over four megabytes = %v, want ErrAnswer", err)
	}

	house.Raw = nil
	house.Forbid = true

	if _, err := sonos.Discover(t.Context(), porch.Client(), porch.Host()); !errors.Is(err, sonos.ErrForbidden) {
		t.Errorf("Discover with UPnP off = %v, want ErrForbidden", err)
	}

	if _, err := sonos.Discover(t.Context(), porch.Client(), ""); !errors.Is(err, sonos.ErrArgument) {
		t.Errorf("Discover of no host = %v, want ErrArgument", err)
	}
}

// errNoSpeaker is what the transport of TestSeed answers.
var errNoSpeaker = errors.New("no speaker here")

// asked is a transport that answers nothing and keeps where each request
// went.
type asked struct{ hosts []string }

func (a *asked) RoundTrip(req *http.Request) (*http.Response, error) {
	a.hosts = append(a.hosts, req.URL.Host)

	return nil, errNoSpeaker
}

// TestSeed: a speaker named without a port is asked on 1400, an IPv6
// address in brackets or not; one named with a port, on that port. What
// the network refuses is none of the module's own errors.
func TestSeed(t *testing.T) {
	t.Parallel()

	transport := &asked{}
	client := &http.Client{Transport: transport}

	for _, seed := range []string{"192.0.2.10", "192.0.2.10:1500", "2001:db8::1", "[2001:db8::1]", "speaker.test"} {
		_, err := sonos.Discover(t.Context(), client, seed)
		if !errors.Is(err, errNoSpeaker) || errors.Is(err, sonos.ErrAnswer) || errors.Is(err, sonos.ErrRefused) ||
			errors.Is(err, sonos.ErrArgument) {
			t.Errorf("Discover of %q with no network = %v", seed, err)
		}
	}

	want := []string{
		"192.0.2.10:1400",
		"192.0.2.10:1500",
		"[2001:db8::1]:1400",
		"[2001:db8::1]:1400",
		"speaker.test:1400",
	}
	if !slices.Equal(transport.hosts, want) {
		t.Errorf("the speakers asked were %v, want %v", transport.hosts, want)
	}
}
