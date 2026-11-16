// Package cli is the sonos command: the rooms of a household and what
// they play, play and pause, volume, and moving a room in and out of a
// group, all asked of the speakers themselves on the LAN.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"

	"github.com/duncanite/go-sonos/sonos"
)

// Exit statuses.
const (
	ExitOK    = 0
	ExitError = 1
)

// EnvHost is the environment variable the address of a speaker is read
// from when --host is not given.
const EnvHost = "SONOS_HOST"

// exitStatus is how the parser's own exit, on --help, leaves Run: kong
// calls its exit function and goes on, so the function panics with the
// status and Run recovers it.
type exitStatus int

type rootCommand struct {
	Host string `env:"SONOS_HOST" help:"The address of any one speaker of the household; it names the others." required:"true"`

	Rooms    roomsCommand    `cmd:"" help:"List the rooms: who plays with whom, what, and how loud."`
	Status   statusCommand   `cmd:"" help:"Print what a room is playing."`
	Play     playCommand     `cmd:"" help:"Start a room, and the rooms grouped with it, playing what it has."`
	Pause    pauseCommand    `cmd:"" help:"Pause a room and the rooms grouped with it."`
	Stop     stopCommand     `cmd:"" help:"Stop a room and the rooms grouped with it."`
	Next     nextCommand     `cmd:"" help:"Skip to the next track."`
	Previous previousCommand `cmd:"" help:"Go back to the previous track."`
	Volume   volumeCommand   `cmd:"" help:"Print a room's volume, or set it."`
	Mute     muteCommand     `cmd:"" help:"Mute a room."`
	Unmute   unmuteCommand   `cmd:"" help:"Unmute a room."`
	Join     joinCommand     `cmd:"" help:"Move a room into another room's group."`
	Leave    leaveCommand    `cmd:"" help:"Take a room out of its group, to play alone."`

	out    io.Writer
	system *sonos.System
}

type room struct {
	Room string `arg:"" help:"The room's name, whatever its case."`
}

type (
	roomsCommand    struct{}
	statusCommand   struct{ room }
	playCommand     struct{ room }
	pauseCommand    struct{ room }
	stopCommand     struct{ room }
	nextCommand     struct{ room }
	previousCommand struct{ room }
	muteCommand     struct{ room }
	unmuteCommand   struct{ room }
	leaveCommand    struct{ room }
	volumeCommand   struct {
		room

		Level *int `arg:""                                                                                         help:"The volume to set, 0 to 100; the volume is printed when left out." optional:""`
		Group bool `help:"The volume of the room's whole group, each room keeping its share, in place of its own."`
	}
	joinCommand struct {
		room

		With string `arg:"" help:"A room of the group to join."`
	}
)

// Run parses args and runs the command. client makes every request to the
// speakers.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, client *http.Client) (status int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			exited, ok := recovered.(exitStatus)
			if !ok {
				panic(recovered)
			}

			status = int(exited)
		}
	}()

	root := rootCommand{out: stdout}

	parser, err := kong.New(&root,
		kong.Name("sonos"),
		kong.Description("Control the Sonos speakers of a household on the LAN. One speaker's address, in --host or "+
			EnvHost+", is enough: it names the others."),
		kong.UsageOnError(),
		kong.Writers(stdout, stderr),
		kong.Exit(func(code int) { panic(exitStatus(code)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(stderr, "sonos:", err)

		return ExitError
	}

	kctx, err := parser.Parse(args)
	if err != nil {
		parser.Errorf("%s", err)

		return ExitError
	}

	if root.system, err = sonos.Discover(ctx, client, root.Host); err == nil {
		err = kctx.Run(&root)
	}

	if err != nil {
		fmt.Fprintln(stderr, "sonos:", err)

		if errors.Is(err, sonos.ErrForbidden) {
			fmt.Fprintln(stderr, "sonos: a speaker answers so with its UPnP setting off: "+
				"in the Sonos app, Account, Privacy and Security, UPnP")
		}

		return ExitError
	}

	return ExitOK
}

func (*roomsCommand) Run(ctx context.Context, root *rootCommand) error {
	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "ROOM\tWITH\tSTATE\tVOLUME\tMUTED\tPLAYING\tADDRESS")

	for _, group := range root.system.Groups {
		status, err := root.system.Status(ctx, group)
		if err != nil {
			return err
		}

		for _, member := range group.Members {
			level, err := root.system.Volume(ctx, member)
			if err != nil {
				return err
			}

			muted, err := root.system.Muted(ctx, member)
			if err != nil {
				return err
			}

			fmt.Fprintf(table, "%q\t%s\t%s\t%d\t%s\t%s\t%s\n",
				member.Name, dash(others(group, member)), said(status.State), level, yesNo(muted),
				dash(playing(status)), member.Host)
		}
	}

	if err := table.Flush(); err != nil {
		return fmt.Errorf("writing the table: %w", err)
	}

	return nil
}

// others is the rooms member plays with; the names are their owner's
// text.
func others(group sonos.Group, member sonos.Player) string {
	var names []string

	for _, other := range group.Members {
		if other.UUID != member.UUID {
			names = append(names, strconv.Quote(other.Name))
		}
	}

	return strings.Join(names, ",")
}

func (cmd *statusCommand) Run(ctx context.Context, root *rootCommand) error {
	_, group, err := root.system.Room(cmd.Room)
	if err != nil {
		return err
	}

	status, err := root.system.Status(ctx, group)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 1, ' ', 0)

	fmt.Fprintf(table, "state:\t%s\n", said(status.State))

	// What the source says of the track is not this command's text.
	for _, field := range [][2]string{
		{"title", status.Title}, {"artist", status.Artist}, {"album", status.Album}, {"source", status.URI},
	} {
		if field[1] != "" {
			fmt.Fprintf(table, "%s:\t%q\n", field[0], field[1])
		}
	}

	switch {
	case status.Duration > 0:
		fmt.Fprintf(table, "position:\t%s of %s\n", clock(status.Position), clock(status.Duration))
	case status.Position > 0:
		fmt.Fprintf(table, "position:\t%s\n", clock(status.Position))
	}

	if err = table.Flush(); err != nil {
		return fmt.Errorf("writing the status: %w", err)
	}

	return nil
}

// transport runs one of the group's transport actions on the room's
// group.
func (root *rootCommand) transport(
	ctx context.Context,
	name string,
	action func(context.Context, sonos.Group) error,
) error {
	_, group, err := root.system.Room(name)
	if err != nil {
		return err
	}

	return action(ctx, group)
}

func (cmd *playCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, root.system.Play)
}

func (cmd *pauseCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, root.system.Pause)
}

func (cmd *stopCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, root.system.Stop)
}

func (cmd *nextCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, root.system.Next)
}

func (cmd *previousCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, root.system.Previous)
}

func (cmd *volumeCommand) Run(ctx context.Context, root *rootCommand) error {
	player, group, err := root.system.Room(cmd.Room)
	if err != nil {
		return err
	}

	switch {
	case cmd.Level != nil && cmd.Group:
		return root.system.SetGroupVolume(ctx, group, *cmd.Level)
	case cmd.Level != nil:
		return root.system.SetVolume(ctx, player, *cmd.Level)
	}

	var level int

	if cmd.Group {
		level, err = root.system.GroupVolume(ctx, group)
	} else {
		level, err = root.system.Volume(ctx, player)
	}

	if err != nil {
		return err
	}

	fmt.Fprintln(root.out, level)

	return nil
}

func (cmd *muteCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.mute(ctx, cmd.Room, true)
}

func (cmd *unmuteCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.mute(ctx, cmd.Room, false)
}

func (root *rootCommand) mute(ctx context.Context, name string, muted bool) error {
	player, _, err := root.system.Room(name)
	if err != nil {
		return err
	}

	return root.system.SetMute(ctx, player, muted)
}

func (cmd *joinCommand) Run(ctx context.Context, root *rootCommand) error {
	player, _, err := root.system.Room(cmd.Room)
	if err != nil {
		return err
	}

	_, group, err := root.system.Room(cmd.With)
	if err != nil {
		return err
	}

	return root.system.Join(ctx, player, group)
}

func (cmd *leaveCommand) Run(ctx context.Context, root *rootCommand) error {
	player, _, err := root.system.Room(cmd.Room)
	if err != nil {
		return err
	}

	return root.system.Leave(ctx, player)
}

// said is a transport state as a person says it; one the documentation
// does not give is printed as the speaker wrote it.
func said(state sonos.State) string {
	switch state {
	case sonos.Playing:
		return "playing"
	case sonos.Paused:
		return "paused"
	case sonos.Stopped:
		return "stopped"
	case sonos.Transitioning:
		return "starting"
	default:
		return strconv.Quote(string(state))
	}
}

// playing is the track in a few words: its title, and its artist when the
// source names one.
func playing(status sonos.Status) string {
	switch {
	case status.Title == "":
		return ""
	case status.Artist == "":
		return strconv.Quote(status.Title)
	default:
		return strconv.Quote(status.Title + " - " + status.Artist)
	}
}

// clock is a time into a track, as hours, minutes and seconds.
func clock(length time.Duration) string {
	seconds := int(length / time.Second)

	const perMinute, perHour = 60, 3600

	return fmt.Sprintf("%d:%02d:%02d", seconds/perHour, seconds%perHour/perMinute, seconds%perMinute)
}

func yesNo(yes bool) string {
	if yes {
		return "yes"
	}

	return "no"
}

func dash(value string) string {
	if value == "" {
		return "-"
	}

	return value
}
