package cli_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/forkcloser/go-upnp/ssdp"
	"github.com/forkcloser/go-upnp/ssdp/ssdptest"

	"github.com/duncanite/go-sonos/internal/cli"
	"github.com/duncanite/go-sonos/sonos/sonostest"
)

// patience is how long a test waits for what the command has to print.
const patience = 10 * time.Second

// printed is what a command under way has written, read while it writes.
type printed struct {
	mu   sync.Mutex
	text bytes.Buffer
}

func (p *printed) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.text.Write(data)
}

func (p *printed) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.text.String()
}

// under is a command under way: what it writes, and how to end it.
type under struct {
	t      *testing.T
	out    *printed
	stop   context.CancelFunc
	status chan int
}

// start runs the command on a network until it is stopped.
func start(t *testing.T, network ssdp.Network, args ...string) *under {
	t.Helper()

	ctx, stop := context.WithCancel(t.Context())
	running := &under{t: t, out: &printed{}, stop: stop, status: make(chan int, 1)}

	go func() {
		running.status <- cli.Run(ctx, cli.Environment{
			Args: args, Out: running.out, Err: running.out, Client: local{}, Network: network,
		})
	}()

	t.Cleanup(stop)

	return running
}

// says waits for the command to have written each of these, the spacing
// and the time of day aside.
func (u *under) says(wants ...string) {
	u.t.Helper()

	for deadline := time.Now().Add(patience); ; time.Sleep(time.Millisecond) {
		written := u.out.String()
		missing := ""

		for _, want := range wants {
			if !strings.Contains(written, want) {
				missing = want
			}
		}

		if missing == "" {
			return
		}

		if time.Now().After(deadline) {
			u.t.Fatalf("the command did not write %q; it wrote\n%s", missing, written)
		}
	}
}

// ends is the status the command exits with.
func (u *under) ends() int {
	u.t.Helper()

	select {
	case status := <-u.status:
		return status
	case <-time.After(patience):
		u.t.Fatalf("the command did not end; it wrote\n%s", u.out.String())
	}

	return 0
}

// TestWatching: watch prints where it listens, then how the household
// stands, its groups, what each plays and how loud each room is, and then
// each change as it happens, until it is interrupted, which is no error.
// With no speaker named it listens at this host's address on the network
// the speakers were found on.
func TestWatching(t *testing.T) {
	t.Parallel()

	house := household(t)
	den := house.Speakers[1]

	for name, running := range map[string]*under{
		"found by search":  start(t, advertised(den), "--wait", "200ms", "watch"),
		"named":            start(t, ssdptest.NewHost(), "--host", den.Host(), "watch"),
		"named by its URL": start(t, ssdptest.NewHost(), "--host", den.URL+"/xml/device_description.xml", "watch"),
		"at a callback":    start(t, ssdptest.NewHost(), "--host", den.Host(), "watch", "--callback", "127.0.0.1:0"),
	} {
		running.says(
			"watching; callback http://127.0.0.1:",
			" groups: Kitchen | Den + Porch\n",
			" Kitchen: stopped -\n",
			" Den: playing A Song - A Band\n",
			" Kitchen: volume 20, muted no\n",
			" Den: volume 20, muted no\n",
			" Porch: volume 35, muted yes\n",
		)

		t.Logf("%s: the household as it stands is printed", name)
	}

	// The first event's time is past before the first change: it bounds
	// the first event and nothing after it.
	running := start(t, ssdptest.NewHost(), "--host", den.Host(), "watch", "--first-event", "300ms")
	running.says(" Porch: volume 35, muted yes\n")

	time.Sleep(time.Second / 2)

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"pause", "Porch"}, " Den: paused A Song - A Band\n"},
		{[]string{"volume", "Porch", "40"}, " Porch: volume 40, muted yes\n"},
		{[]string{"unmute", "Porch"}, " Porch: volume 40, muted no\n"},
		{[]string{"join", "Kitchen", "Den"}, " groups: Kitchen + Den + Porch\n"},
		{[]string{"leave", "Porch"}, " groups: Kitchen + Den | Porch\n"},
	} {
		if _, errs, status := run(t, house, step.args...); status != cli.ExitOK {
			t.Fatalf("%v exits %d saying %q", step.args, status, errs)
		}

		running.says(step.want)
	}

	running.stop()

	if status := running.ends(); status != cli.ExitOK {
		t.Errorf("watch, interrupted, exits %d having written\n%s", status, running.out.String())
	}
}

// TestWatchingFails: speakers that take the subscription and send nothing
// by the first event's time cannot reach the callback, and watch says so;
// a callback that is no host and port, and a household that takes no
// subscription, are errors too.
func TestWatchingFails(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen")
	t.Cleanup(house.Close)

	host := house.Speakers[0].Host()

	house.Silent = true

	running := start(t, ssdptest.NewHost(), "--host", host, "watch", "--first-event", "100ms")
	if status := running.ends(); status != cli.ExitError ||
		!strings.Contains(
			running.out.String(),
			"no event within 100ms: the speakers cannot reach the callback http://127.0.0.1:",
		) {
		t.Errorf("watch of speakers that send nothing exits %d having written\n%s", status, running.out.String())
	}

	house.Silent, house.Deaf = false, true

	running = start(t, ssdptest.NewHost(), "--host", host, "watch")
	if status := running.ends(); status != cli.ExitError ||
		!strings.Contains(running.out.String(), "HTTP 503") {
		t.Errorf(
			"watch of speakers that take no subscription exits %d having written\n%s",
			status,
			running.out.String(),
		)
	}

	house.Deaf = false

	running = start(t, ssdptest.NewHost(), "--host", host, "watch", "--callback", "nowhere")
	if status := running.ends(); status != cli.ExitError || !strings.Contains(running.out.String(), "nowhere") {
		t.Errorf("watch at a callback that is none exits %d having written\n%s", status, running.out.String())
	}
}

// TestASpeakersWords: what a speaker says is shown and never obeyed. A
// room's name is written out wherever it is printed, and so is what a
// speaker says in refusing: a line's end in it is no line of the
// command's.
func TestASpeakersWords(t *testing.T) {
	t.Parallel()

	const hostile = "Den\nsonos: all is well\u202e\u009b2J"

	house := sonostest.New(hostile, "Porch")
	t.Cleanup(house.Close)

	shown := `Den\nsonos: all is well\u202e\u009b2J`

	out, errs, status := run(t, house, "rooms")
	if status != cli.ExitOK || errs != "" || strings.Count(out, "\n") != 3 || !strings.Contains(out, shown) {
		t.Errorf("rooms of a room with a hostile name exits %d, saying %q and\n%s", status, errs, out)
	}

	_, errs, status = run(t, house, "play", "Garage")
	if status != cli.ExitError || strings.Count(errs, "\n") != 1 || !strings.Contains(errs, shown) {
		t.Errorf("a room that is none, beside a hostile name, exits %d saying %q", status, errs)
	}

	running := start(t, ssdptest.NewHost(), "--host", house.Speakers[0].Host(), "watch")
	running.says(" groups: "+shown+" | Porch\n", " "+shown+": volume 20, muted no\n")
	running.stop()

	if status = running.ends(); status != cli.ExitOK || strings.Contains(running.out.String(), "\nsonos:") {
		t.Errorf("watch of a room with a hostile name exits %d having written\n%s", status, running.out.String())
	}

	house.Raw = []byte(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault>` +
		`<faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>` +
		`<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>714</errorCode>` +
		"<errorDescription>no\nsonos: all is well\u202e</errorDescription></UPnPError></detail></s:Fault></s:Body></s:Envelope>")

	_, errs, status = run(t, house, "rooms")
	if status != cli.ExitError || strings.Count(errs, "\n") != 1 ||
		!strings.Contains(errs, `UPnP error 714, no\nsonos: all is well\u202e`) {
		t.Errorf("a refusal in a speaker's words exits %d saying %q", status, errs)
	}
}
