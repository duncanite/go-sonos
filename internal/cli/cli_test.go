package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/duncanite/go-sonos/internal/cli"
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

	var stdout, stderr bytes.Buffer

	args = append([]string{"--host", house.Speakers[0].Host()}, args...)
	status = cli.Run(t.Context(), args, &stdout, &stderr, house.Speakers[0].Client())

	return stdout.String(), stderr.String(), status
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

	var out, errs bytes.Buffer

	if status := cli.Run(t.Context(), []string{"rooms"}, &out, &errs, porch.Client()); status != cli.ExitError ||
		!strings.Contains(errs.String(), "--host") {
		t.Errorf("with no speaker named, the command exits %d saying %q", status, errs.String())
	}

	house.Forbid = true

	_, said, status := run(t, house, "rooms")
	if status != cli.ExitError || !strings.Contains(said, "forbids UPnP control") ||
		!strings.Contains(said, "Privacy and Security, UPnP") {
		t.Errorf("with UPnP off, rooms exits %d saying %q", status, said)
	}

	out.Reset()

	if status = cli.Run(t.Context(), []string{"--help"}, &out, &errs, porch.Client()); status != cli.ExitOK ||
		!strings.Contains(out.String(), cli.EnvHost) {
		t.Errorf("--help exits %d saying %q", status, out.String())
	}
}
