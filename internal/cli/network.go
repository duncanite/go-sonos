package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"slices"
	"text/tabwriter"

	"github.com/forkcloser/go-upnp/ssdp"
	"github.com/mycophonic/primordium/fault"

	"github.com/duncanite/go-sonos/internal/terminal"
	"github.com/duncanite/go-sonos/sonos"
)

// lent is the host's SSDP as the library asks for it.
type lent struct {
	browser *ssdp.Browser
}

func (l lent) Browse(ctx context.Context, target string, handle func(sonos.Advertisement, bool)) error {
	//nolint:wrapcheck // the Browser's own word, which the library hands on as it is
	return l.browser.Browse(ctx, target, func(change ssdp.Change, heard ssdp.Advertisement) {
		locations := make([]sonos.Location, 0, len(heard.Locations))
		for _, location := range heard.Locations {
			locations = append(locations, sonos.Location{URL: location.URL, Local: location.Local})
		}

		handle(sonos.Advertisement{USN: heard.USN, Locations: locations}, change == ssdp.Lost)
	})
}

// search finds the speakers that answer within the wait, in the order of
// their ids.
func (root *rootCommand) search(ctx context.Context) ([]sonos.Found, error) {
	cfg := ssdp.Config{Network: root.network}

	if root.Interface != "" {
		cfg.Interfaces = func(candidate ssdp.Interface) bool { return candidate.Name == root.Interface }
	}

	browser, err := ssdp.Open(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening the network: %w", err)
	}

	defer func() { _ = browser.Close() }()

	searching, cancel := context.WithTimeout(ctx, root.Wait)
	defer cancel()

	found := make(map[sonos.ID]sonos.Found)

	err = sonos.Find(searching, lent{browser: browser}, func(speaker sonos.Found, lost bool) {
		if lost {
			delete(found, speaker.ID)
		} else {
			found[speaker.ID] = speaker
		}
	})

	if ctx.Err() != nil {
		return nil, fmt.Errorf("searching for speakers: %w", ctx.Err())
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("searching for speakers: %w", err)
	}

	speakers := make([]sonos.Found, 0, len(found))
	for _, id := range slices.Sorted(maps.Keys(found)) {
		speakers = append(speakers, found[id])
	}

	return speakers, nil
}

// household is the household the command speaks to, asked once: of the
// speaker --host names, or of the first speaker a search finds that
// answers.
func (root *rootCommand) household(ctx context.Context) (*sonos.System, error) {
	if root.system != nil {
		return root.system, nil
	}

	if root.Host != "" {
		system, err := sonos.Open(ctx, root.client, root.Host)
		if err != nil {
			return nil, err
		}

		root.system = system

		return system, nil
	}

	speakers, err := root.search(ctx)
	if err != nil {
		return nil, err
	}

	return root.first(ctx, speakers)
}

// first asks the speakers found, in order, each at every place it was
// heard at, and keeps the household of the first that answers.
func (root *rootCommand) first(ctx context.Context, speakers []sonos.Found) (*sonos.System, error) {
	failure := fmt.Errorf("%w: no speaker answered the search within %s; name one with --host",
		fault.ErrNotFound, root.Wait)

	for _, speaker := range speakers {
		for _, location := range speaker.Locations {
			system, err := sonos.Open(ctx, root.client, location.URL)
			if err != nil {
				failure = fmt.Errorf("%s: %w", location.URL, err)

				continue
			}

			root.system, root.local = system, location.Local

			return system, nil
		}
	}

	return nil, failure
}

// Run finds the speakers on this network and prints each with the room it
// is: none for a speaker that is no room, the second of a pair or a sub,
// and for one of another household than the first that answered.
func (*discoverCommand) Run(ctx context.Context, root *rootCommand) error {
	speakers, err := root.search(ctx)
	if err != nil {
		return err
	}

	if len(speakers) == 0 {
		terminal.Printf(root.out, "No speaker answered.\n")

		return nil
	}

	rooms := make(map[sonos.ID]string)

	// A household that will not open hides no speaker: each is printed
	// with no room, and the reason after them.
	system, opening := root.first(ctx, speakers)
	if opening == nil {
		for _, group := range system.Groups {
			for _, member := range group.Members {
				rooms[member.ID] = member.Name
			}
		}
	}

	table := tabwriter.NewWriter(root.out, 0, 0, 2, ' ', 0)

	terminal.Printf(table, "SPEAKER\tROOM\tAT\tFROM\n")

	for _, speaker := range speakers {
		where, from := "", ""

		// The first place it was heard at: the one the household is
		// asked at.
		if len(speaker.Locations) > 0 {
			where, from = speaker.Locations[0].URL, address(speaker.Locations[0].Local)
		}

		terminal.Printf(table, "%s\t%s\t%s\t%s\n", speaker.ID, dash(rooms[speaker.ID]), dash(where), dash(from))
	}

	if err = table.Flush(); err != nil {
		return fmt.Errorf("writing the speakers: %w", err)
	}

	return opening
}

// address is an address as it is written, and nothing when it is none.
func address(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}

	return addr.String()
}
