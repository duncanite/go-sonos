package sonos

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	upnp "github.com/forkcloser/go-upnp"
)

// State is what a group's transport is doing, in the speakers' words.
type State string

// The states the documentation gives.
const (
	Playing       State = "PLAYING"
	Paused        State = "PAUSED_PLAYBACK"
	Stopped       State = "STOPPED"
	Transitioning State = "TRANSITIONING"
)

// maxVolume is the top of a speaker's volume.
const maxVolume = 100

// Status is what a group is playing.
type Status struct {
	State State
	// Title, Artist and Album are the track's, as far as its source says.
	Title  string
	Artist string
	Album  string
	// URI is the track's address, a stream's or a line-in's among them.
	URI string
	// Position and Duration are zero where the source has none: a radio
	// stream, a line-in.
	Position time.Duration
	Duration time.Duration
}

// Status reads what the group plays, from its coordinator.
func (s *System) Status(ctx context.Context, group Group) (Status, error) {
	transport, err := s.transport(ctx, group.Coordinator, "GetTransportInfo")
	if err != nil {
		return Status{}, err
	}

	position, err := s.transport(ctx, group.Coordinator, "GetPositionInfo")
	if err != nil {
		return Status{}, err
	}

	status := Status{
		State:    State(value(transport, "CurrentTransportState")),
		URI:      value(position, "TrackURI"),
		Position: clock(value(position, "RelTime")),
		Duration: clock(value(position, "TrackDuration")),
	}

	status.Title, status.Artist, status.Album = described(value(position, "TrackMetaData"))

	return status, nil
}

// described reads a track's description, a DIDL-Lite document inside an
// answer or an event: its title, artist and album. A source that has none
// sends an empty string or NOT_IMPLEMENTED, which read as none; and a
// speaker with nothing to play has been seen to tell, in an event, a title
// that is a space, which is none either.
func described(document string) (title, artist, album string) {
	var track struct {
		Title  string `xml:"item>title"`
		Artist string `xml:"item>creator"`
		Album  string `xml:"item>album"`
	}

	if xml.Unmarshal([]byte(document), &track) != nil {
		return "", "", ""
	}

	return strings.TrimSpace(track.Title), strings.TrimSpace(track.Artist), strings.TrimSpace(track.Album)
}

// Play starts the group playing what it has.
func (s *System) Play(ctx context.Context, group Group) error {
	_, err := s.transport(ctx, group.Coordinator, "Play", upnp.Argument{Name: "Speed", Value: "1"})

	return err
}

// Pause pauses the group.
func (s *System) Pause(ctx context.Context, group Group) error {
	_, err := s.transport(ctx, group.Coordinator, "Pause")

	return err
}

// Stop stops the group.
func (s *System) Stop(ctx context.Context, group Group) error {
	_, err := s.transport(ctx, group.Coordinator, "Stop")

	return err
}

// Next skips to the next track.
func (s *System) Next(ctx context.Context, group Group) error {
	_, err := s.transport(ctx, group.Coordinator, "Next")

	return err
}

// Previous goes back to the previous track.
func (s *System) Previous(ctx context.Context, group Group) error {
	_, err := s.transport(ctx, group.Coordinator, "Previous")

	return err
}

// Volume is the player's own volume, 0 to 100.
func (s *System) Volume(ctx context.Context, player Player) (int, error) {
	answer, err := call(ctx, s.client, player.Host, renderingType, renderingPath, "GetVolume",
		instance(), master())
	if err != nil {
		return 0, err
	}

	return number(answer, "CurrentVolume")
}

// SetVolume sets the player's own volume, 0 to 100.
func (s *System) SetVolume(ctx context.Context, player Player, level int) error {
	if level < 0 || level > maxVolume {
		return fmt.Errorf("%w: a volume of %d", ErrArgument, level)
	}

	_, err := call(ctx, s.client, player.Host, renderingType, renderingPath, "SetVolume",
		instance(), master(), upnp.Argument{Name: "DesiredVolume", Value: strconv.Itoa(level)})

	return err
}

// Muted is whether the player is muted.
func (s *System) Muted(ctx context.Context, player Player) (bool, error) {
	answer, err := call(ctx, s.client, player.Host, renderingType, renderingPath, "GetMute",
		instance(), master())
	if err != nil {
		return false, err
	}

	return value(answer, "CurrentMute") == "1", nil
}

// SetMute mutes the player or not.
func (s *System) SetMute(ctx context.Context, player Player, muted bool) error {
	desired := "0"
	if muted {
		desired = "1"
	}

	_, err := call(ctx, s.client, player.Host, renderingType, renderingPath, "SetMute",
		instance(), master(), upnp.Argument{Name: "DesiredMute", Value: desired})

	return err
}

// GroupVolume is the volume of the group as a whole.
func (s *System) GroupVolume(ctx context.Context, group Group) (int, error) {
	answer, err := call(ctx, s.client, group.Coordinator.Host, groupRenderingType, groupRenderingPath,
		"GetGroupVolume", instance())
	if err != nil {
		return 0, err
	}

	return number(answer, "CurrentVolume")
}

// SetGroupVolume sets the volume of the group as a whole, 0 to 100, each
// player keeping its share: the speakers move them in the ratio of the
// last snapshot, which is taken first.
func (s *System) SetGroupVolume(ctx context.Context, group Group, level int) error {
	if level < 0 || level > maxVolume {
		return fmt.Errorf("%w: a volume of %d", ErrArgument, level)
	}

	host := group.Coordinator.Host

	if _, err := call(ctx, s.client, host, groupRenderingType, groupRenderingPath,
		"SnapshotGroupVolume", instance()); err != nil {
		return err
	}

	_, err := call(ctx, s.client, host, groupRenderingType, groupRenderingPath, "SetGroupVolume",
		instance(), upnp.Argument{Name: "DesiredVolume", Value: strconv.Itoa(level)})

	return err
}

// Join moves the player into the group: it plays what the group's
// coordinator plays. The System is stale afterwards.
func (s *System) Join(ctx context.Context, player Player, group Group) error {
	if player.ID == group.Coordinator.ID {
		return fmt.Errorf("%w: %q coordinates the group it would join", ErrArgument, player.Name)
	}

	_, err := s.transport(ctx, player, "SetAVTransportURI",
		upnp.Argument{Name: "CurrentURI", Value: "x-rincon:" + string(group.Coordinator.ID)},
		upnp.Argument{Name: "CurrentURIMetaData"})

	return err
}

// Leave takes the player out of its group, to play alone. The System is
// stale afterwards.
func (s *System) Leave(ctx context.Context, player Player) error {
	_, err := s.transport(ctx, player, "BecomeCoordinatorOfStandaloneGroup")

	return err
}

// transport calls an action of the transport service on a player.
func (s *System) transport(
	ctx context.Context,
	player Player,
	action string,
	args ...upnp.Argument,
) (upnp.Arguments, error) {
	return call(ctx, s.client, player.Host, transportType, transportPath, action,
		append([]upnp.Argument{instance()}, args...)...)
}

// value is one value of an answer, empty when the answer has none by that
// name.
func value(answer upnp.Arguments, name string) string {
	written, _ := answer.Get(name)

	return written
}

// number reads an integer out of an answer.
func number(answer upnp.Arguments, name string) (int, error) {
	level, err := strconv.Atoi(value(answer, name))
	if err != nil {
		return 0, fmt.Errorf("%w: %s is %q", ErrAnswer, name, value(answer, name))
	}

	return level, nil
}

// clock reads a time as the transport writes it, hours, minutes and
// seconds; what is no such time, a stream's NOT_IMPLEMENTED, is zero.
func clock(written string) time.Duration {
	parts := strings.Split(written, ":")

	const fields = 3
	if len(parts) != fields {
		return 0
	}

	var total time.Duration

	for index, unit := range []time.Duration{time.Hour, time.Minute, time.Second} {
		value, err := strconv.Atoi(parts[index])
		if err != nil || value < 0 {
			return 0
		}

		total += time.Duration(value) * unit
	}

	return total
}
