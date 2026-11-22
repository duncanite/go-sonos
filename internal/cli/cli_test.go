package cli_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/forkcloser/go-upnp/ssdp"
	"github.com/forkcloser/go-upnp/ssdp/ssdptest"

	"github.com/duncanite/go-sonos/internal/cli"
	"github.com/duncanite/go-sonos/sonos"
	"github.com/duncanite/go-sonos/sonos/sonostest"
)

// household is three rooms: Kitchen alone, Den coordinating Porch, the den
// playing a track.
func household(t *testing.T) *sonostest.Household {
	t.Helper()

	house := sonostest.New("Kitchen", "Den", "Porch")
	t.Cleanup(house.Close)

	den := house.Speakers[1]
	house.Speakers[2].Group = den.UUID
	house.Speakers[2].Volume = 35
	house.Speakers[2].Muted = true

	house.State[den.UUID] = "PLAYING"
	house.URI[den.UUID] = "x-sonos-http:track.mp4"
	house.Position[den.UUID] = "0:01:05"
	house.Duration[den.UUID] = "0:03:20"
	house.Track[den.UUID] = `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"><item>` +
		`<dc:title>A Song</dc:title><dc:creator>A Band</dc:creator></item></DIDL-Lite>`

	return house
}

// run is the command, given the first speaker as the one that names the
// others; its output and what it wrote of errors, then its status.
func run(t *testing.T, house *sonostest.Household, args ...string) (out, errs string, status int) {
	t.Helper()

	return runOn(t, ssdptest.NewHost(), append([]string{"--host", house.Speakers[0].Host()}, args...)...)
}

// runOn is the command on a network, with no speaker named unless the
// arguments name one.
func runOn(t *testing.T, network ssdp.Network, args ...string) (out, errs string, status int) {
	t.Helper()

	var stdout, stderr bytes.Buffer

	status = cli.Run(t.Context(), cli.Environment{
		Args: args, Out: &stdout, Err: &stderr, Client: local{}, Network: network,
	})

	return stdout.String(), stderr.String(), status
}

// errNobody is what local returns for a host that is not this one.
var errNobody = errors.New("nobody answers there")

// local is a client that reaches the loopback, where the speakers of
// these tests are, and no other host: a test that wants a host nobody
// answers on names one that is not there, and asks nothing of the ports
// of the host it runs on.
type local struct{}

func (local) Do(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() != "127.0.0.1" {
		return nil, errNobody
	}

	return http.DefaultClient.Do(request)
}

// advertised is a network the speakers are advertised on, each at the
// loopback address its server listens at, and this host on it. A network
// serves one run of the command.
func advertised(speakers ...*sonostest.Speaker) *ssdptest.Host {
	lan, host := ssdptest.NewLAN(), ssdptest.NewHost()

	host.Plug(lan, "lo0", 1, netip.MustParsePrefix("127.0.0.1/8"))

	for _, speaker := range speakers {
		lan.Device(netip.MustParseAddr("127.0.0.1"), ssdptest.Advert{
			USN:      "uuid:" + speaker.UUID + "::" + sonos.SearchTarget,
			Type:     sonos.SearchTarget,
			Location: speaker.URL + "/xml/device_description.xml",
		})
	}

	return host
}

// rows is the output as lines of fields, the spacing of a table aside.
func rows(out string) []string {
	var lines []string

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		lines = append(lines, strings.Join(strings.Fields(line), " "))
	}

	return lines
}

// TestReading: the rooms as a table, each with the rooms it plays with,
// its own volume and its group's track; a room's status, asked of any
// room of its group.
func TestReading(t *testing.T) {
	t.Parallel()

	house := household(t)
	kitchen, den, porch := house.Speakers[0], house.Speakers[1], house.Speakers[2]

	for _, step := range []struct {
		args []string
		want []string
	}{
		{[]string{"rooms"}, []string{
			"ROOM WITH STATE VOLUME MUTED PLAYING ADDRESS",
			`"Kitchen" - stopped 20 no - ` + kitchen.Host(),
			`"Den" "Porch" playing 20 no "A Song - A Band" ` + den.Host(),
			`"Porch" "Den" playing 35 yes "A Song - A Band" ` + porch.Host(),
		}},
		{[]string{"status", "porch"}, []string{
			"state: playing",
			`title: "A Song"`,
			`artist: "A Band"`,
			`source: "x-sonos-http:track.mp4"`,
			"position: 0:01:05 of 0:03:20",
		}},
		{[]string{"status", "Kitchen"}, []string{"state: stopped"}},
		{[]string{"volume", "Porch"}, []string{"35"}},
		{[]string{"volume", "Porch", "--group"}, []string{"27"}},
		{[]string{"household"}, []string{`"` + house.ID + `"`}},
	} {
		out, errs, status := run(t, house, step.args...)
		if got := rows(
			out,
		); status != cli.ExitOK || errs != "" ||
			strings.Join(got, "\n") != strings.Join(step.want, "\n") {
			t.Errorf("%v exits %d, saying %q and\n%s\nwant\n%s",
				step.args, status, errs, strings.Join(got, "\n"), strings.Join(step.want, "\n"))
		}
	}
}

// TestActing: each command that changes something does it, on the speaker
// it belongs to, and prints nothing.
func TestActing(t *testing.T) {
	t.Parallel()

	house := household(t)
	kitchen, den, porch := house.Speakers[0], house.Speakers[1], house.Speakers[2]

	for _, step := range []struct {
		args []string
		done func() bool
	}{
		{[]string{"pause", "Porch"}, func() bool { return house.State[den.UUID] == "PAUSED_PLAYBACK" }},
		{[]string{"play", "porch"}, func() bool { return house.State[den.UUID] == "PLAYING" }},
		{[]string{"next", "Den"}, func() bool { return last(den) == "AVTransport.Next" }},
		{[]string{"previous", "Porch"}, func() bool { return last(den) == "AVTransport.Previous" }},
		{[]string{"stop", "Porch"}, func() bool { return house.State[den.UUID] == "STOPPED" }},
		{[]string{"volume", "Porch", "0"}, func() bool { return porch.Volume == 0 && den.Volume == 20 }},
		{[]string{"volume", "Porch", "40", "--group"}, func() bool { return porch.Volume == 40 && den.Volume == 40 }},
		{[]string{"unmute", "Porch"}, func() bool { return !porch.Muted }},
		{[]string{"mute", "Den"}, func() bool { return den.Muted && !porch.Muted }},
		{[]string{"join", "Kitchen", "Porch"}, func() bool { return kitchen.Group == den.UUID }},
		{[]string{"leave", "Porch"}, func() bool { return porch.Group == porch.UUID && kitchen.Group == den.UUID }},
	} {
		out, errs, status := run(t, house, step.args...)
		if status != cli.ExitOK || out != "" || errs != "" || !step.done() {
			t.Fatalf("%v exits %d, saying %q and %q, done: %t", step.args, status, out, errs, step.done())
		}
	}
}

func last(speaker *sonostest.Speaker) string {
	received := speaker.Received()
	if len(received) == 0 {
		return ""
	}

	return received[len(received)-1]
}

// TestRefusing: what the command cannot do, it says on the error stream
// and exits 1: a room that is none, with the rooms there are; a volume no
// speaker takes, with nothing sent; no speaker to ask; a household with
// UPnP off, with where the setting is. --help exits 0.
func TestRefusing(t *testing.T) {
	t.Parallel()

	house := household(t)
	porch := house.Speakers[2]

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"play", "Garage"}, `no room by that name: "Garage"; there are "Kitchen", "Den", "Porch"`},
		{[]string{"join", "Porch", "Garage"}, `no room by that name: "Garage"`},
		{[]string{"join", "Den", "Porch"}, `"Den" coordinates the group it would join`},
		{[]string{"volume", "Porch", "101"}, "not a valid argument: a volume of 101"},
		{[]string{"volume", "Porch", "loud"}, "loud"},
		{[]string{"status"}, `expected "<room>"`},
	} {
		before := len(porch.Received())

		out, errs, status := run(t, house, step.args...)
		if status != cli.ExitError || out != "" || !strings.Contains(errs, step.want) {
			t.Errorf("%v exits %d, saying %q and %q, want an error with %q", step.args, status, out, errs, step.want)
		}

		if sent := porch.Received()[before:]; len(sent) != 0 {
			t.Errorf("%v sent %v", step.args, sent)
		}
	}

	if _, said, status := runOn(t, ssdptest.NewHost(), "--wait", "50ms", "rooms"); status != cli.ExitError ||
		!strings.Contains(said, "no speaker answered the search within 50ms; name one with --host") {
		t.Errorf("with no speaker named and none on the network, the command exits %d saying %q", status, said)
	}

	if _, said, status := runOn(t, ssdptest.NewHost(), "--host", "192.0.2.1", "rooms"); status != cli.ExitError ||
		!strings.Contains(said, "192.0.2.1:1400") || !strings.Contains(said, "nobody answers there") {
		t.Errorf("with a speaker that is not there, the command exits %d saying %q", status, said)
	}

	house.Forbid = true

	_, said, status := run(t, house, "rooms")
	if status != cli.ExitError || !strings.Contains(said, "forbids UPnP control") ||
		!strings.Contains(said, "Privacy and Security, UPnP") {
		t.Errorf("with UPnP off, rooms exits %d saying %q", status, said)
	}

	if out, _, status := runOn(t, ssdptest.NewHost(), "--help"); status != cli.ExitOK ||
		!strings.Contains(out, cli.EnvHost) {
		t.Errorf("--help exits %d saying %q", status, out)
	}
}
