package cli_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/forkcloser/go-upnp/ssdp/ssdptest"

	"github.com/duncanite/go-sonos/internal/cli"
	"github.com/duncanite/go-sonos/sonos/sonostest"
)

// TestDiscover: every speaker that answers the search is listed, in the
// order of their ids, with the room it is, where it is and this host's
// address there; a speaker that is no room has none, and what is no
// speaker is not listed. A network with no speaker says so.
func TestDiscover(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen", "Den", "Sub")
	t.Cleanup(house.Close)

	kitchen, den, sub := house.Speakers[0], house.Speakers[1], house.Speakers[2]
	sub.Group, sub.Invisible = den.UUID, true

	network := advertised(den, sub, kitchen)

	out, errs, status := runOn(t, network, "--wait", "200ms", "discover")

	want := []string{
		"SPEAKER ROOM AT FROM",
		kitchen.UUID + " Kitchen " + kitchen.URL + "/xml/device_description.xml 127.0.0.1",
		den.UUID + " Den " + den.URL + "/xml/device_description.xml 127.0.0.1",
		sub.UUID + " - " + sub.URL + "/xml/device_description.xml 127.0.0.1",
	}
	if got := rows(out); status != cli.ExitOK || errs != "" || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf(
			"discover exits %d, saying %q and\n%s\nwant\n%s",
			status,
			errs,
			strings.Join(got, "\n"),
			strings.Join(want, "\n"),
		)
	}

	// Another maker's device on the network, and no speaker.
	lan, host := ssdptest.NewLAN(), ssdptest.NewHost()
	host.Plug(lan, "lo0", 1, netip.MustParsePrefix("127.0.0.1/8"))
	lan.Device(netip.MustParseAddr("127.0.0.1"), ssdptest.Advert{
		USN:      "uuid:Socket-1_0-A::urn:Belkin:device:controllee:1",
		Type:     "urn:Belkin:device:controllee:1",
		Location: kitchen.URL + "/setup.xml",
	})

	if out, errs, status = runOn(t, host, "--wait", "200ms", "discover"); status != cli.ExitOK || errs != "" ||
		out != "No speaker answered.\n" {
		t.Errorf("discover with no speaker exits %d, saying %q and %q", status, errs, out)
	}
}

// TestDiscoverUnopened: speakers that answer the search and none of which
// gives the household are listed all the same, with no room, and the
// command fails with why.
func TestDiscoverUnopened(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen")
	t.Cleanup(house.Close)

	kitchen := house.Speakers[0]
	house.Forbid = true

	out, errs, status := runOn(t, advertised(kitchen), "--wait", "200ms", "discover")

	want := kitchen.UUID + " - " + kitchen.URL + "/xml/device_description.xml 127.0.0.1"
	if got := rows(out); status != cli.ExitError || len(got) != 2 || got[1] != want ||
		!strings.Contains(errs, "forbids UPnP control") || !strings.Contains(errs, "Privacy and Security, UPnP") {
		t.Errorf("discover of a household with UPnP off exits %d, saying %q and\n%s", status, errs, out)
	}
}

// TestSearched: with no speaker named, a command asks the first speaker
// the search finds, and that speaker names the others.
func TestSearched(t *testing.T) {
	t.Parallel()

	house := household(t)
	kitchen, den, porch := house.Speakers[0], house.Speakers[1], house.Speakers[2]

	out, errs, status := runOn(t, advertised(porch), "--wait", "200ms", "rooms")

	want := []string{
		"ROOM WITH STATE VOLUME MUTED PLAYING ADDRESS",
		"Kitchen - stopped 20 no - " + kitchen.Host(),
		"Den Porch playing 20 no A Song - A Band " + den.Host(),
		"Porch Den playing 35 yes A Song - A Band " + porch.Host(),
	}
	if got := rows(out); status != cli.ExitOK || errs != "" || strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rooms by search exits %d, saying %q and\n%s", status, errs, strings.Join(got, "\n"))
	}

	if got := porch.Received(); len(got) == 0 || got[0] != "ZoneGroupTopology.GetZoneGroupState" {
		t.Errorf("the speaker found was asked %v, want the household first", got)
	}

	if _, errs, status = runOn(
		t,
		advertised(porch),
		"--wait",
		"200ms",
		"--interface",
		"en9",
		"rooms",
	); status != cli.ExitError ||
		!strings.Contains(errs, "no speaker answered the search") {
		t.Errorf("a search on an interface the speakers are not on exits %d, saying %q", status, errs)
	}
}
