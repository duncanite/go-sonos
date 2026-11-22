package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-sonos/sonos"
)

// Run watches the household and prints what it says as it comes, until the
// context ends. The first lines are how it stands: its groups, what each
// plays, how loud each room is.
func (cmd *watchCommand) Run(ctx context.Context, root *rootCommand) error {
	system, err := root.household(ctx)
	if err != nil {
		return err
	}

	listen, err := cmd.listen(ctx, root)
	if err != nil {
		return err
	}

	events, err := sonos.Listen(ctx, listen)
	if err != nil {
		return err
	}

	defer func() { _ = events.Close() }()

	// The speakers reach the listener at the host it was asked to listen
	// at, and the port it took.
	reached, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("%w: the callback %q is not host:port", fault.ErrInvalidArgument, listen)
	}

	callback := "http://" + net.JoinHostPort(reached, strconv.Itoa(int(events.Addr().Port()))) + "/"

	fmt.Fprintf(root.out, "watching; callback %s\n", callback)

	// The household's first event, its groups, comes within a second of
	// the Watch on its own network; with none by the first event's time
	// the speakers never reached the callback, and that is an error with
	// the reason, not a wait.
	watching, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	unreached := fmt.Errorf("%w: no event within %s: the speakers cannot reach the callback %s",
		fault.ErrTimeout, cmd.FirstEvent, callback)

	late := time.AfterFunc(cmd.FirstEvent, func() { cancel(unreached) })
	defer late.Stop()

	said := &printer{out: root.out, rooms: make(map[sonos.ID]string)}

	err = system.Watch(watching, events, callback, func(event sonos.Event) {
		late.Stop()
		said.print(event)
	})

	switch {
	case ctx.Err() != nil:
		return nil
	case errors.Is(context.Cause(watching), unreached):
		return unreached
	default:
		return err
	}
}

// listen is where the listener listens: the callback given, or this host's
// address on the speakers' network, as the search said it or as the way to
// the speaker asked gives it, and a free port.
func (cmd *watchCommand) listen(ctx context.Context, root *rootCommand) (string, error) {
	if cmd.Callback != "" {
		return cmd.Callback, nil
	}

	if root.local.IsValid() {
		return net.JoinHostPort(root.local.String(), "0"), nil
	}

	// --host may be the URL of a speaker's description.
	asked := root.Host
	if location, err := url.Parse(asked); err == nil && location.Host != "" {
		asked = location.Host
	}

	toward, err := localHostToward(ctx, asked)
	if err != nil {
		return "", err
	}

	return net.JoinHostPort(toward, "0"), nil
}

// printer prints a household's events, each on a line, and keeps the
// rooms' names to say whose each is.
type printer struct {
	out   io.Writer
	rooms map[sonos.ID]string
}

func (p *printer) print(event sonos.Event) {
	now := time.Now().Format(time.TimeOnly)

	switch told := event.(type) {
	case sonos.Regrouped:
		clear(p.rooms)

		groups := make([]string, 0, len(told.Groups))

		for _, group := range told.Groups {
			names := make([]string, 0, len(group.Members))

			for _, member := range group.Members {
				p.rooms[member.ID] = member.Name
				names = append(names, strconv.Quote(member.Name))
			}

			groups = append(groups, strings.Join(names, "+"))
		}

		fmt.Fprintf(p.out, "%s groups: %s\n", now, dash(strings.Join(groups, " | ")))
	case sonos.Played:
		fmt.Fprintf(p.out, "%s %s: %s %s\n", now, p.room(told.Coordinator), said(told.Status.State),
			dash(playing(told.Status)))
	case sonos.Sounded:
		fmt.Fprintf(p.out, "%s %s: volume %d, muted %s\n", now, p.room(told.Player), told.Volume, yesNo(told.Muted))
	}
}

// room is a speaker by the name of its room, or by its id when it is no
// room: the second of a pair that coordinates a group.
func (p *printer) room(id sonos.ID) string {
	if name, known := p.rooms[id]; known {
		return strconv.Quote(name)
	}

	return strconv.Quote(string(id))
}

// localHostToward is this host's address on the way to host, read off a
// connected UDP socket, which sends nothing: the callback URL has to name
// an address the speakers can reach, and a host with several interfaces
// has one per network.
func localHostToward(ctx context.Context, host string) (string, error) {
	target := host
	if _, _, err := net.SplitHostPort(host); err != nil {
		target = net.JoinHostPort(strings.Trim(host, "[]"), "9")
	}

	conn, err := (&net.Dialer{}).DialContext(ctx, "udp4", target)
	if err != nil {
		return "", fmt.Errorf("%w: finding this host's address toward %s: %w", fault.ErrNetworkCommunication, host, err)
	}

	defer func() { _ = conn.Close() }()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("%w: the local address is not a UDP address", fault.ErrSystemFailure)
	}

	return addr.IP.String(), nil
}
