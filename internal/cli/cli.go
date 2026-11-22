// Package cli is the sonos command: the speakers on this network, the
// rooms of a household and what they play, play and pause, volume, moving
// a room in and out of a group, and what the household does as it happens,
// all asked of the speakers themselves on the LAN.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"
	"github.com/forkcloser/go-upnp/ssdp"

	"github.com/duncanite/go-sonos/internal/terminal"
	"github.com/duncanite/go-sonos/sonos"
)

// Exit statuses.
const (
	ExitOK    = 0
	ExitError = 1
)

// EnvHost is the environment variable the address of a speaker is read
// from when --host is not given; with neither, the speakers are searched
// for.
const EnvHost = "SONOS_HOST"

// exitStatus is how the parser's own exit, on --help, leaves Run: kong
// calls its exit function and goes on, so the function panics with the
// status and Run recovers it.
type exitStatus int

type rootCommand struct {
	Host      string        `env:"SONOS_HOST"      help:"The address of any one speaker of the household; it names the others. Without it, the speakers are searched for, and the first that answers is asked."`
	Interface string        `env:"SONOS_INTERFACE" help:"The network interface the speakers are on, when the host has several."`
	Wait      time.Duration `default:"3s"          env:"SONOS_WAIT"                                                                                                                                             help:"How long a search collects answers."`

	Discover  discoverCommand  `cmd:"" help:"Find every speaker on this network."`
	Household householdCommand `cmd:"" help:"Print the household's identity."`
	Watch     watchCommand     `cmd:"" help:"Print what the household does as it happens, until interrupted."`

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

	out     io.Writer
	client  sonos.Doer
	network ssdp.Network

	// system is the household, once a command asked for it, and local
	// this host's address on its network when a search said.
	system *sonos.System
	local  netip.Addr
}

// Environment is where the command runs.
type Environment struct {
	// Args is its arguments, without its name.
	Args []string
	// Out and Err are where it writes.
	Out io.Writer
	Err io.Writer
	// Client makes every request to the speakers. It has to follow no
	// redirect.
	Client sonos.Doer
	// Network is where speakers are searched for; nil is the host's own.
	Network ssdp.Network
}

type room struct {
	Room string `arg:"" help:"The room's name, whatever its case."`
}

type (
	discoverCommand  struct{}
	householdCommand struct{}
	watchCommand     struct {
		Callback   string        `help:"host:port to listen at, which the speakers notify; empty picks this host's address on the speakers' network and a free port."`
		FirstEvent time.Duration `default:"10s"                                                                                                                       help:"How long the speakers get to send a first event, how the household stands, before the callback is judged unreachable."`
	}
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

// Run parses the arguments and runs the command.
func Run(ctx context.Context, env Environment) (status int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			exited, ok := recovered.(exitStatus)
			if !ok {
				panic(recovered)
			}

			status = int(exited)
		}
	}()

	root := rootCommand{out: env.Out, client: env.Client, network: env.Network}

	parser, err := kong.New(&root,
		kong.Name("sonos"),
		kong.Description("Control the Sonos speakers of a household on the LAN. One speaker's address, in --host or "+
			EnvHost+", is enough: it names the others. With none, the speakers are searched for."),
		kong.UsageOnError(),
		kong.Writers(env.Out, env.Err),
		kong.Exit(func(code int) { panic(exitStatus(code)) }),
		kong.BindTo(ctx, (*context.Context)(nil)),
	)
	if err != nil {
		fmt.Fprintln(env.Err, "sonos:", err)

		return ExitError
	}

	kctx, err := parser.Parse(env.Args)
	if err != nil {
		parser.Errorf("%s", err)

		return ExitError
	}

	if err = kctx.Run(&root); err != nil {
		// An error may carry a speaker's words: what it said in refusing.
		fmt.Fprintln(env.Err, "sonos:", terminal.Text(err.Error()))

		if errors.Is(err, sonos.ErrForbidden) {
			fmt.Fprintln(env.Err, "sonos: a speaker answers so with its UPnP setting off: "+
				"in the Sonos app, Account, Privacy and Security, UPnP")
		}

		return ExitError
	}

	return ExitOK
}

func (*roomsCommand) Run(ctx context.Context, root *rootCommand) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(table, "ROOM\tWITH\tSTATE\tVOLUME\tMUTED\tPLAYING\tADDRESS")

	for _, group := range system.Groups {
		status, err := system.Status(ctx, group)
		if err != nil {
			return err
		}

		for _, member := range group.Members {
			level, err := system.Volume(ctx, member)
			if err != nil {
				return err
			}

			muted, err := system.Muted(ctx, member)
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
		if other.ID != member.ID {
			names = append(names, strconv.Quote(other.Name))
		}
	}

	return strings.Join(names, ",")
}

func (cmd *statusCommand) Run(ctx context.Context, root *rootCommand) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	_, group, err := system.Room(cmd.Room)
	if err != nil {
		return err
	}

	status, err := system.Status(ctx, group)
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
	action func(*sonos.System, context.Context, sonos.Group) error,
) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	_, group, err := system.Room(name)
	if err != nil {
		return err
	}

	return action(system, ctx, group)
}

func (cmd *playCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, (*sonos.System).Play)
}

func (cmd *pauseCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, (*sonos.System).Pause)
}

func (cmd *stopCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, (*sonos.System).Stop)
}

func (cmd *nextCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, (*sonos.System).Next)
}

func (cmd *previousCommand) Run(ctx context.Context, root *rootCommand) error {
	return root.transport(ctx, cmd.Room, (*sonos.System).Previous)
}

func (cmd *volumeCommand) Run(ctx context.Context, root *rootCommand) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	player, group, err := system.Room(cmd.Room)
	if err != nil {
		return err
	}

	switch {
	case cmd.Level != nil && cmd.Group:
		return system.SetGroupVolume(ctx, group, *cmd.Level)
	case cmd.Level != nil:
		return system.SetVolume(ctx, player, *cmd.Level)
	}

	var level int

	if cmd.Group {
		level, err = system.GroupVolume(ctx, group)
	} else {
		level, err = system.Volume(ctx, player)
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
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	player, _, err := system.Room(name)
	if err != nil {
		return err
	}

	return system.SetMute(ctx, player, muted)
}

func (cmd *joinCommand) Run(ctx context.Context, root *rootCommand) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	player, _, err := system.Room(cmd.Room)
	if err != nil {
		return err
	}

	_, group, err := system.Room(cmd.With)
	if err != nil {
		return err
	}

	return system.Join(ctx, player, group)
}

func (cmd *leaveCommand) Run(ctx context.Context, root *rootCommand) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	player, _, err := system.Room(cmd.Room)
	if err != nil {
		return err
	}

	return system.Leave(ctx, player)
}

func (*householdCommand) Run(ctx context.Context, root *rootCommand) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	household, err := system.Household(ctx)
	if err != nil {
		return err
	}

	fmt.Fprintf(root.out, "%q\n", household)

	return nil
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
