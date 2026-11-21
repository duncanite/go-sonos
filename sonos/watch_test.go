package sonos_test

// The contract of events, from the community's documentation of the
// services (https://sonos.svrooij.io/services/) and from SoCo's reader of
// them (https://github.com/SoCo/SoCo, soco/events_base.py):
//
//   - Each service takes subscriptions at its event URL, the control URL
//     with Event for Control.
//   - ZoneGroupTopology tells ZoneGroupState, the document
//     GetZoneGroupState gives, among other variables.
//   - AVTransport and RenderingControl tell one variable, LastChange: a
//     document with the variables that changed, each an element named
//     after it with its value in val, under an InstanceID element; a
//     volume and a mute come once per channel, Master being the speaker's.
//     SoCo reads a value from the element's text when val is not there.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/duncanite/go-sonos/sonos"
	"github.com/duncanite/go-sonos/sonos/sonostest"
)

// patience is how long a test waits for what has to come.
const patience = 10 * time.Second

// watch is one Watch under way.
type watch struct {
	t      *testing.T
	events chan sonos.Event
	stop   context.CancelFunc
	done   chan struct{}
	err    error
}

// listening is a listener on the loopback and the URL speakers reach it
// at.
func listening(t *testing.T) (*sonos.Events, string) {
	t.Helper()

	events, err := sonos.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	t.Cleanup(func() { _ = events.Close() })

	return events, "http://" + events.Addr().String() + "/"
}

// follow watches the household on a listener of its own.
func follow(t *testing.T, system *sonos.System) *watch {
	t.Helper()

	events, callback := listening(t)

	return followOn(t, system, events, callback)
}

// followOn watches the household on a listener.
func followOn(t *testing.T, system *sonos.System, events *sonos.Events, callback string) *watch {
	t.Helper()

	ctx, stop := context.WithCancel(t.Context())
	following := &watch{t: t, events: make(chan sonos.Event, 64), stop: stop, done: make(chan struct{})}

	go func() {
		defer close(following.done)

		following.err = system.Watch(ctx, events, callback, func(event sonos.Event) {
			select {
			case following.events <- event:
			case <-ctx.Done():
			}
		})
	}()

	// The Watch is over before the household it watches is closed.
	t.Cleanup(func() {
		stop()
		<-following.done
	})

	return following
}

// next is the next event told.
func (w *watch) next() sonos.Event {
	w.t.Helper()

	select {
	case event := <-w.events:
		return event
	case <-w.done:
		w.t.Fatalf("the Watch ended with %v where an event was due", w.err)
	case <-time.After(patience):
		w.t.Fatal("no event came")
	}

	return nil
}

// several is the next events told, in an order of their own: what several
// speakers say comes in no order.
func (w *watch) several(count int) []string {
	w.t.Helper()

	told := make([]string, 0, count)
	for range count {
		event := w.next()
		told = append(told, fmt.Sprintf("%T%+v", event, event))
	}

	slices.Sort(told)

	return told
}

// ended is what the Watch returned.
func (w *watch) ended() error {
	w.t.Helper()

	select {
	case <-w.done:
		return w.err
	case <-time.After(patience):
		w.t.Fatal("the Watch did not end")
	}

	return nil
}

// track is a DIDL-Lite document for a title.
func track(title string) string {
	return `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" ` +
		`xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/"><item id="-1" parentID="-1">` +
		`<dc:title>` + title + `</dc:title><dc:creator>A Band</dc:creator>` +
		`<upnp:album>An Album</upnp:album></item></DIDL-Lite>`
}

// played is three rooms, Kitchen alone and Den coordinating Porch, the
// den playing a track and the porch at 35 and muted; with its speakers'
// ids.
type played struct {
	house               *sonostest.Household
	system              *sonos.System
	kitchen, den, porch sonos.ID
}

func playing(t *testing.T) played {
	t.Helper()

	house := sonostest.New("Kitchen", "Den", "Porch")
	t.Cleanup(house.Close)

	kitchen, den, porch := house.Speakers[0], house.Speakers[1], house.Speakers[2]
	porch.Group = den.UUID
	porch.Volume, porch.Muted = 35, true

	house.State[den.UUID] = "PLAYING"
	house.URI[den.UUID] = "x-sonos-http:track.mp4"
	house.Position[den.UUID] = "0:01:05"
	house.Duration[den.UUID] = "0:03:20"
	house.Track[den.UUID] = track("A Song")

	return played{
		house: house, system: opened(t, house),
		kitchen: sonos.ID(kitchen.UUID), den: sonos.ID(den.UUID), porch: sonos.ID(porch.UUID),
	}
}

// song is what the den of playing plays, as a Watch tells it: no position.
func song() sonos.Status {
	return sonos.Status{
		State: sonos.Playing, Title: "A Song", Artist: "A Band", Album: "An Album",
		URI: "x-sonos-http:track.mp4", Duration: 3*time.Minute + 20*time.Second,
	}
}

// shown is events as several gives them.
func shown(events ...sonos.Event) []string {
	told := make([]string, 0, len(events))
	for _, event := range events {
		told = append(told, fmt.Sprintf("%T%+v", event, event))
	}

	slices.Sort(told)

	return told
}

// standing takes the events a Watch of playing starts with, and fails
// when they are not how the household stands.
func standing(t *testing.T, following *watch, home played) {
	t.Helper()

	kitchen, den, porch := home.kitchen, home.den, home.porch

	first, is := following.next().(sonos.Regrouped)
	if !is || !reflect.DeepEqual(first.Groups, home.system.Groups) {
		t.Fatalf("the Watch starts with %+v, want the groups %+v", first, home.system.Groups)
	}

	want := shown(
		sonos.Played{Coordinator: kitchen, Status: sonos.Status{State: sonos.Stopped}},
		sonos.Played{Coordinator: den, Status: song()},
		sonos.Sounded{Player: kitchen, Volume: 20},
		sonos.Sounded{Player: den, Volume: 20},
		sonos.Sounded{Player: porch, Volume: 35, Muted: true},
	)

	if got := following.several(len(want)); !slices.Equal(got, want) {
		t.Fatalf("after its groups the Watch tells\n%v\nwant\n%v", got, want)
	}
}

// TestWatch: a Watch tells first the groups, then what each group plays,
// from its coordinator, and how loud each room is, the speaker's own
// channel and no other; then each change, whole: a pause keeps the track,
// a mute keeps the volume. What changes nothing told is not told. The
// handler may ask things of the System. When the Watch ends, no speaker
// holds a subscription.
func TestWatch(t *testing.T) {
	t.Parallel()

	home := playing(t)
	house, system, den, porch := home.house, home.system, home.den, home.porch
	following := follow(t, system)

	standing(t, following, home)

	_, group := room(t, system, "Porch")
	player, _ := room(t, system, "Porch")

	paused := song()
	paused.State = sonos.Paused

	for _, step := range []struct {
		name string
		do   func() error
		want sonos.Event
	}{
		{"a pause", func() error { return system.Pause(t.Context(), group) }, sonos.Played{Coordinator: den, Status: paused}},
		{
			"a volume", func() error { return system.SetVolume(t.Context(), player, 40) },
			sonos.Sounded{Player: porch, Volume: 40, Muted: true},
		},
		{
			"an unmute", func() error { return system.SetMute(t.Context(), player, false) },
			sonos.Sounded{Player: porch, Volume: 40},
		},
		// Nothing of these is told: a pause of what is paused, the same
		// volume again, a variable that is not told of, the groups as
		// they are, and everything said again as it stands.
		{"what changes nothing", func() error {
			house.Speakers[2].Event(sonostest.Rendering, [2]string{
				"LastChange",
				`<Event xmlns="urn:schemas-upnp-org:metadata-1-0/RCS/"><InstanceID val="0"><Bass val="3"/></InstanceID></Event>`,
			})
			house.Speakers[0].Event(sonostest.Topology, [2]string{"ZoneGroupName", "Kitchen"})
			house.Speakers[1].Event(sonostest.Transport, [2]string{"Other", "thing"})
			house.Publish()

			return errors.Join(system.Pause(t.Context(), group), system.SetVolume(t.Context(), player, 40),
				system.SetVolume(t.Context(), player, 41))
		}, sonos.Sounded{Player: porch, Volume: 41}},
		// A value written as the element's text, as SoCo reads it.
		{"a volume in text", func() error {
			house.Speakers[2].Event(sonostest.Rendering, [2]string{
				"LastChange",
				`<Event xmlns="urn:schemas-upnp-org:metadata-1-0/RCS/"><InstanceID val="0">` +
					`<Volume channel="Master">12</Volume><Volume channel="LF">100</Volume></InstanceID></Event>`,
			})

			return nil
		}, sonos.Sounded{Player: porch, Volume: 12}},
		// A title that is a space, as a speaker with nothing to play has
		// been seen to tell: no title.
		{"a title of nothing", func() error {
			house.Speakers[1].Event(sonostest.Transport, [2]string{
				"LastChange",
				`<Event xmlns="urn:schemas-upnp-org:metadata-1-0/AVT/"><InstanceID val="0">` +
					`<CurrentTrackMetaData val="` + html.EscapeString(track(" ")) + `"/></InstanceID></Event>`,
			})

			return nil
		}, sonos.Played{Coordinator: den, Status: sonos.Status{
			State: sonos.Paused, Artist: "A Band", Album: "An Album",
			URI: "x-sonos-http:track.mp4", Duration: 3*time.Minute + 20*time.Second,
		}}},
		// A track that changes, with no word of the state.
		{"another track", func() error {
			house.Speakers[1].Event(sonostest.Transport, [2]string{
				"LastChange",
				`<Event xmlns="urn:schemas-upnp-org:metadata-1-0/AVT/"><InstanceID val="0">` +
					`<CurrentTrackURI val="x-rincon-mp3radio://radio.test/stream"/>` +
					`<CurrentTrackDuration val="NOT_IMPLEMENTED"/><CurrentTrackMetaData val=""/></InstanceID></Event>`,
			})

			return nil
		}, sonos.Played{Coordinator: den, Status: sonos.Status{State: sonos.Paused, URI: "x-rincon-mp3radio://radio.test/stream"}}},
	} {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}

		if got := following.next(); !reflect.DeepEqual(got, step.want) {
			t.Fatalf("%s is told as %+v, want %+v", step.name, got, step.want)
		}
	}

	following.stop()

	if err := following.ended(); !errors.Is(err, context.Canceled) {
		t.Errorf("a Watch whose context ended returns %v, want the context's error", err)
	}

	for _, speaker := range house.Speakers {
		for _, service := range []string{sonostest.Topology, sonostest.Transport, sonostest.Rendering} {
			if held := speaker.Subscribers(service); held != 0 {
				t.Errorf("after the Watch %s holds %d subscriptions to %s", speaker.Name, held, service)
			}
		}
	}

	// The groups are asked of the speaker the System was opened on, a
	// group's transport of its coordinator, a room's volume of that room.
	for index, want := range [][]string{
		{"SUBSCRIBE ZoneGroupTopology", "SUBSCRIBE AVTransport", "SUBSCRIBE RenderingControl"},
		{"SUBSCRIBE AVTransport", "SUBSCRIBE RenderingControl"},
		{"SUBSCRIBE RenderingControl"},
	} {
		asked := house.Speakers[index].Subscribed()

		for _, request := range want {
			if !slices.Contains(asked, request) {
				t.Errorf("%s was asked %v, without %q", house.Speakers[index].Name, asked, request)
			}
		}

		if len(asked) != 2*len(want) {
			t.Errorf("%s was asked %v, want %v and the end of each", house.Speakers[index].Name, asked, want)
		}
	}
}

// TestWatchHandler: the handler may take its time and ask things of the
// System: what it asks is answered while the Watch waits for it.
func TestWatchHandler(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen")
	t.Cleanup(house.Close)

	system := opened(t, house)
	events, callback := listening(t)

	type asked struct {
		level int
		err   error
	}

	answers := make(chan asked, 8)

	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = system.Watch(ctx, events, callback, func(event sonos.Event) {
			if sound, is := event.(sonos.Sounded); is {
				player, _, _ := system.Room("Kitchen")
				level, err := system.Volume(ctx, player)

				answers <- asked{level: level + sound.Volume, err: err}
			}
		})
	}()

	t.Cleanup(func() {
		stop()
		<-done
	})

	select {
	case answer := <-answers:
		if answer.err != nil || answer.level != 40 {
			t.Errorf("the handler asked the volume and got %+v, want 20 twice", answer)
		}
	case <-time.After(patience):
		t.Fatal("the handler was not told how loud the kitchen is")
	}
}

// TestWatchRegrouped: a room that joins a group is told with the groups,
// and its transport, which is its coordinator's from then on, is watched
// no more; a room that leaves is told with the groups, and what it plays
// alone is told from how it stands. A room that is gone from the household
// is watched no more, and told from how it stands when it is back.
func TestWatchRegrouped(t *testing.T) {
	t.Parallel()

	home := playing(t)
	house, system, kitchen, den := home.house, home.system, home.kitchen, home.den
	following := follow(t, system)

	standing(t, following, home)

	player, _ := room(t, system, "Kitchen")
	_, group := room(t, system, "Porch")

	if err := system.Join(t.Context(), player, group); err != nil {
		t.Fatalf("Join: %v", err)
	}

	joined, is := following.next().(sonos.Regrouped)
	if !is || len(joined.Groups) != 1 || joined.Groups[0].Coordinator.ID != den || len(joined.Groups[0].Members) != 3 {
		t.Fatalf("the join is told as %+v, want one group of three under the den", joined)
	}

	// The handler's groups are its own: what it does to them changes
	// nothing told later, and the same groups said again are not told.
	joined.Groups[0].Members[0].Name = "Changed"

	house.Publish()

	if err := system.SetVolume(t.Context(), player, 21); err != nil {
		t.Fatalf("SetVolume: %v", err)
	}

	if got, want := following.next(), (sonos.Sounded{Player: kitchen, Volume: 21}); got != want {
		t.Fatalf("after the groups were said again, unchanged, the next event is %+v, want %+v", got, want)
	}

	for deadline := time.Now().Add(patience); house.Speakers[0].Subscribers(sonostest.Transport) != 0; {
		if time.Now().After(deadline) {
			t.Fatal("the kitchen's transport is still watched after it joined the den")
		}

		time.Sleep(time.Millisecond)
	}

	if held := house.Speakers[0].Subscribers(sonostest.Rendering); held != 1 {
		t.Errorf("the kitchen's volume is watched by %d subscriptions after the join, want the one", held)
	}

	// The groups as they are after the join take every action.
	if err := system.Play(t.Context(), joined.Groups[0]); err != nil {
		t.Fatalf("Play on the group as it was told: %v", err)
	}

	// The den was playing: only what is said of the kitchen comes next,
	// and it is said though it is what the kitchen played before.
	if err := system.Leave(t.Context(), player); err != nil {
		t.Fatalf("Leave: %v", err)
	}

	left, is := following.next().(sonos.Regrouped)
	if !is || !reflect.DeepEqual(left.Groups, system.Groups) {
		t.Fatalf("the leave is told as %+v, want the groups as they were, %+v", left, system.Groups)
	}

	want := sonos.Played{Coordinator: kitchen, Status: sonos.Status{State: sonos.Stopped}}
	if got := following.next(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after it left, the kitchen is told as %+v, want %+v", got, want)
	}

	// The porch is no room any more, then a room again, as loud as it was.
	porch := house.Speakers[2]
	porch.Invisible = true

	house.Publish()

	gone, is := following.next().(sonos.Regrouped)
	if !is || len(gone.Groups) != 2 || len(gone.Groups[1].Members) != 1 || gone.Groups[1].Members[0].ID != den {
		t.Fatalf("a room that is gone is told as %+v, want the den alone in its group", gone)
	}

	for deadline := time.Now().Add(patience); porch.Subscribers(sonostest.Rendering) != 0; {
		if time.Now().After(deadline) {
			t.Fatal("the porch's volume is still watched after it was gone")
		}

		time.Sleep(time.Millisecond)
	}

	porch.Invisible = false

	house.Publish()

	if back, is := following.next().(sonos.Regrouped); !is || !reflect.DeepEqual(back.Groups, system.Groups) {
		t.Fatalf("a room that is back is told as %+v, want the groups as they were", back)
	}

	if got, want := following.next(), (sonos.Sounded{Player: home.porch, Volume: 35, Muted: true}); got != want {
		t.Fatalf("a room that is back is told as %+v, want %+v", got, want)
	}
}

// TestWatchEnds: a Watch ends with why. An event that is not in the
// documented shape is ErrAnswer; an event that did not arrive is ErrLost;
// a speaker that takes no subscription is ErrRefused, and ErrForbidden
// with UPnP off; a listener that is closed is ErrClosed, before the Watch
// or under it; a Watch with no listener or no handler is ErrArgument.
// Whatever ends it, no subscription is left.
func TestWatchEnds(t *testing.T) {
	t.Parallel()

	rendering := func(variables string) [2]string {
		return [2]string{
			"LastChange",
			`<Event xmlns="urn:schemas-upnp-org:metadata-1-0/RCS/"><InstanceID val="0">` + variables + `</InstanceID></Event>`,
		}
	}

	for _, row := range []struct {
		name string
		do   func(house *sonostest.Household)
		want error
	}{
		{"a LastChange that is no XML", func(house *sonostest.Household) {
			house.Speakers[0].Event(sonostest.Rendering, [2]string{"LastChange", "<Event><InstanceID"})
		}, sonos.ErrAnswer},
		{"a volume that is no number", func(house *sonostest.Household) {
			house.Speakers[0].Event(sonostest.Rendering, rendering(`<Volume channel="Master" val="loud"/>`))
		}, sonos.ErrAnswer},
		{"a transport's LastChange that is no XML", func(house *sonostest.Household) {
			house.Speakers[0].Event(sonostest.Transport, [2]string{"LastChange", "PLAYING"})
		}, sonos.ErrAnswer},
		{"groups that are no XML", func(house *sonostest.Household) {
			house.Speakers[0].Event(sonostest.Topology, [2]string{"ZoneGroupState", "<ZoneGroups"})
		}, sonos.ErrAnswer},
		{"a group whose coordinator is no member", func(house *sonostest.Household) {
			house.Speakers[0].Event(sonostest.Topology, [2]string{
				"ZoneGroupState",
				`<ZoneGroups><ZoneGroup Coordinator="A" ID="A:1">` +
					`<ZoneGroupMember UUID="B" Location="http://127.0.0.1:1400/x" ZoneName="Room"/></ZoneGroup></ZoneGroups>`,
			})
		}, sonos.ErrAnswer},
		{"an event that did not arrive", func(house *sonostest.Household) {
			house.Speakers[0].Skip(sonostest.Rendering)
			house.Speakers[0].Event(sonostest.Rendering, rendering(`<Volume channel="Master" val="30"/>`))
		}, sonos.ErrLost},
	} {
		house := sonostest.New("Kitchen")
		t.Cleanup(house.Close)

		following := follow(t, opened(t, house))

		// How the kitchen stands: its groups, what it plays, how loud.
		following.next()
		following.several(2)

		row.do(house)

		if err := following.ended(); !errors.Is(err, row.want) {
			t.Errorf("after %s the Watch ends with %v, want %v", row.name, err, row.want)
		}

		for _, service := range []string{sonostest.Topology, sonostest.Transport, sonostest.Rendering} {
			if held := house.Speakers[0].Subscribers(service); held != 0 {
				t.Errorf("after %s the kitchen holds %d subscriptions to %s", row.name, held, service)
			}
		}
	}

	house := sonostest.New("Kitchen")
	t.Cleanup(house.Close)

	system := opened(t, house)

	house.Deaf = true

	if err := follow(t, system).ended(); !errors.Is(err, sonos.ErrRefused) || errors.Is(err, sonos.ErrForbidden) {
		t.Errorf("a Watch of a speaker that takes no subscription ends with %v, want ErrRefused", err)
	}

	house.Deaf, house.Forbid = false, true

	if err := follow(t, system).ended(); !errors.Is(err, sonos.ErrForbidden) {
		t.Errorf("a Watch of a household with UPnP off ends with %v, want ErrForbidden", err)
	}

	house.Forbid = false

	events, callback := listening(t)
	following := followOn(t, system, events, callback)

	following.next()

	if err := events.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := following.ended(); !errors.Is(err, sonos.ErrClosed) {
		t.Errorf("a Watch whose listener was closed ends with %v, want ErrClosed", err)
	}

	if held := house.Speakers[0].Subscribers(sonostest.Topology); held != 0 {
		t.Errorf("after its listener closed the kitchen holds %d subscriptions", held)
	}

	asked := len(house.Speakers[0].Subscribed())

	for name, err := range map[string]error{
		"a closed listener": system.Watch(t.Context(), events, callback, func(sonos.Event) {}),
	} {
		if !errors.Is(err, sonos.ErrClosed) {
			t.Errorf("a Watch on %s = %v, want ErrClosed", name, err)
		}
	}

	open, _ := listening(t)

	for name, err := range map[string]error{
		"no listener": system.Watch(t.Context(), nil, callback, func(sonos.Event) {}),
		"no handler":  system.Watch(t.Context(), open, callback, nil),
	} {
		if !errors.Is(err, sonos.ErrArgument) {
			t.Errorf("a Watch with %s = %v, want ErrArgument", name, err)
		}
	}

	if got := len(house.Speakers[0].Subscribed()); got != asked {
		t.Errorf("the Watches that could not start asked the kitchen %v", house.Speakers[0].Subscribed()[asked:])
	}
}

// TestWatchUnreached: a speaker the household lists and that cannot be
// reached ends the Watch of all, with the client's own error, which is
// none of this package's. What the speakers that were reached hold then is
// not asked: a subscription being taken as the Watch ends is left to its
// speaker.
func TestWatchUnreached(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen", "Den")
	t.Cleanup(house.Close)

	system := opened(t, house)

	// The den is unplugged, and the kitchen still lists it.
	house.Speakers[1].Close()

	err := follow(t, system).ended()

	for name, known := range map[string]error{
		"ErrLost": sonos.ErrLost, "ErrRefused": sonos.ErrRefused, "ErrAnswer": sonos.ErrAnswer,
		"ErrClosed": sonos.ErrClosed, "ErrArgument": sonos.ErrArgument, "the context's": context.Canceled,
	} {
		if errors.Is(err, known) {
			t.Errorf("a Watch of a speaker that cannot be reached ends with %v, which is %s", err, name)
		}
	}

	if err == nil || !strings.Contains(err.Error(), house.Speakers[1].Host()) {
		t.Errorf("a Watch of a speaker that cannot be reached ends with %v, want the client's error, naming it", err)
	}
}

// TestListen: the listener listens where it was told and says where; an
// address that cannot be listened at is an error; it is done when closed,
// with ErrClosed, and closing it again is nothing.
func TestListen(t *testing.T) {
	t.Parallel()

	events, err := sonos.Listen(t.Context(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	if addr := events.Addr(); addr.Addr().String() != "127.0.0.1" || addr.Port() == 0 {
		t.Errorf("Addr = %s, want the loopback and the port taken", addr)
	}

	select {
	case <-events.Done():
		t.Fatalf("the listener is done before it is closed: %v", events.Err())
	default:
	}

	if events.Err() != nil {
		t.Errorf("Err of a listener that lives = %v", events.Err())
	}

	if _, err = sonos.Listen(t.Context(), events.Addr().String()); err == nil {
		t.Error("Listen at an address already taken gave no error")
	}

	if _, err = sonos.Listen(t.Context(), "not an address"); err == nil {
		t.Error("Listen at what is no address gave no error")
	}

	if err = errors.Join(events.Close(), events.Close()); err != nil {
		t.Errorf("Close, twice = %v", err)
	}

	select {
	case <-events.Done():
	case <-time.After(patience):
		t.Fatal("the listener is not done after Close")
	}

	if !errors.Is(events.Err(), sonos.ErrClosed) {
		t.Errorf("Err after Close = %v, want ErrClosed", events.Err())
	}
}

// TestHousehold: the household's identity is the speaker's word for it,
// and one that gives none is ErrAnswer.
func TestHousehold(t *testing.T) {
	t.Parallel()

	house := sonostest.New("Kitchen")
	t.Cleanup(house.Close)

	system := opened(t, house)

	if household, err := system.Household(t.Context()); err != nil || household != house.ID {
		t.Errorf("Household = %q, %v, want %q", household, err, house.ID)
	}

	if got := house.Speakers[0].Received(); got[len(got)-1] != "DeviceProperties.GetHouseholdID" {
		t.Errorf("the household was asked for as %v", got)
	}

	house.ID = ""

	if household, err := system.Household(t.Context()); !errors.Is(err, sonos.ErrAnswer) {
		t.Errorf("Household of a speaker that names none = %q, %v, want ErrAnswer", household, err)
	}
}
