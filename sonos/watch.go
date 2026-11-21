package sonos

import (
	"context"
	"encoding/xml"
	"fmt"
	"slices"
	"strconv"
	"sync"

	upnp "github.com/forkcloser/go-upnp"
)

// Event is one thing a household says of itself, unasked: a Regrouped, a
// Played or a Sounded.
type Event interface {
	event()
}

// Regrouped is the groups of the household, every one that has a room in
// it: how they stand when a Watch starts, and again, whole, each time they
// change. Its groups are as a System's, and every action of a System takes
// them.
type Regrouped struct {
	Groups []Group
}

// Played is what a group plays, as its coordinator tells it: how it stands
// when the group is first watched, and again, whole, each time the state or
// the track changes.
type Played struct {
	// Coordinator is the group's coordinator.
	Coordinator ID
	// Status is everything the coordinator has said so far. Its Position
	// is always zero: a speaker's events say nothing of where in the
	// track it is, and System.Status asks.
	Status Status
}

// Sounded is how loud a room is, its own volume and mute: how they stand
// when the room is first watched, and again, both, each time one changes.
type Sounded struct {
	// Player is the room's speaker.
	Player ID
	Volume int
	Muted  bool
}

func (Regrouped) event() {}
func (Played) event()    {}
func (Sounded) event()   {}

// Watch tells handle what the household says of itself, for as long as ctx
// lasts: first its groups, a Regrouped, then for each group what it plays,
// a Played, and for each room how loud it is, a Sounded; and after that
// each of them again as it changes. It follows the household as it is
// regrouped: a room that joins is watched from then on, and one that
// leaves, or is gone, no more. The groups come from the speaker the System
// was opened on, what a group plays from its coordinator, a room's volume
// from that room.
//
// Events come one at a time, on the goroutine that called Watch; handle may
// ask things of the System, and may take its time, up to a point: the
// speakers' events wait for it, and more than sixty-four waiting from one
// service of one speaker end the Watch with an ErrLost. Several may watch
// at once.
//
// events is the listener the speakers send them to, and callback the URL
// they reach it at: "http://ADDRESS:PORT/", this host's address on the
// speakers' network, which a Found gives as Local, and the listener's
// port. A speaker that takes a subscription and cannot reach the callback
// sends nothing: the caller holds the deadline on the first event.
//
// Watch returns ctx's error when ctx ends. Sooner, it returns ErrClosed
// when the listener was closed; an ErrRefused when a speaker takes no
// subscription; an ErrLost when a subscription can be trusted no more, or
// the listener ended by itself; an ErrAnswer when a speaker's event is not
// in the documented shape; the client's own error when a speaker cannot be
// reached. One speaker ends the Watch of all: one that went silent is an
// ErrLost, and for as long as the household still lists it, watching again
// ends at once, with the client's error for a speaker it cannot reach.
// Nothing subscribes again by itself: watching again does, and starts from
// how things stand.
//
// Every subscription a speaker confirmed is ended before Watch returns,
// and when the speaker is watched no more. One cut short while it was
// being taken, by groups that changed again at once or by the Watch's
// end, is left to the speaker, which lets go of it at its next event,
// answered as a subscription nobody holds, or when it runs out.
func (s *System) Watch(ctx context.Context, events *Events, callback string, handle func(Event)) error {
	if events == nil || handle == nil {
		return fmt.Errorf("%w: a Watch needs a listener and a handler", ErrArgument)
	}

	if err := events.Err(); err != nil {
		return err
	}

	watching, cancel := context.WithCancel(ctx)
	defer cancel()

	// The listener's life is not the caller's context: the Watch ends
	// with whichever ends first.
	stop := context.AfterFunc(events.over, cancel) //nolint:contextcheck // see above
	defer stop()

	following := &following{
		system:   s,
		events:   events,
		callback: callback,
		handle:   handle,
		told:     make(chan told),
		ended:    make(chan ended),
		quit:     make(chan struct{}),
		held:     make(map[subject]*subscription),
		playing:  make(map[ID]Status),
		sounding: make(map[ID]Sounded),
	}

	err := following.run(watching)

	cancel()
	close(following.quit)
	following.running.Wait()

	switch over := events.Err(); {
	case ctx.Err() != nil:
		return ctx.Err() //nolint:wrapcheck // the caller's own context, as it ended
	case over != nil:
		return over
	default:
		return err
	}
}

// subject is what one subscription is about: a service of a speaker.
type subject struct {
	// id is the speaker's; the speaker the topology is asked of has none.
	id   ID
	host string
	kind string
}

// subscription is one subscription held; a speaker's word counts only
// while the subscription it came under is the one held for its subject.
type subscription struct {
	end context.CancelFunc
}

type (
	// told is one event of a subscription.
	told struct {
		from  *subscription
		about subject
		event upnp.Event
	}
	// ended is a subscription that is over, and why.
	ended struct {
		from  *subscription
		about subject
		err   error
	}
)

// following is one Watch. Everything but the channels and the group is
// the goroutine's that called Watch, so nothing here is locked.
type following struct {
	system   *System
	events   *Events
	callback string
	handle   func(Event)

	told    chan told
	ended   chan ended
	quit    chan struct{}
	running sync.WaitGroup

	held map[subject]*subscription

	// groups is the household as it was last told, once regrouped is
	// true; playing and sounding are what each speaker watched has said.
	groups    []Group
	regrouped bool
	playing   map[ID]Status
	sounding  map[ID]Sounded
}

// run follows the household until ctx ends or a subscription does.
func (f *following) run(ctx context.Context) error {
	f.subscribe(ctx, subject{host: f.system.seed, kind: topologyType})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err() //nolint:wrapcheck // Watch says which end this was
		case one := <-f.told:
			if f.held[one.about] != one.from {
				continue
			}

			if err := f.take(ctx, one); err != nil {
				return fmt.Errorf("an event of %s: %w", one.about.host, err)
			}
		case one := <-f.ended:
			if f.held[one.about] == one.from {
				return fmt.Errorf("the events of %s: %w", one.about.host, own(one.err))
			}
		}
	}
}

// subscribe starts a subscription, on a goroutine of its own that hands
// each event to the Watch's and ends with the subscription.
func (f *following) subscribe(ctx context.Context, about subject) {
	subscribed, end := context.WithCancel(ctx)
	one := &subscription{end: end}
	f.held[about] = one

	service := at(about.host, about.kind, under(about.kind))

	f.running.Go(func() {
		defer end()

		err := f.events.subscriber.Watch(subscribed, f.system.client, service, f.callback, func(event upnp.Event) {
			select {
			case f.told <- told{from: one, about: about, event: event}:
			case <-subscribed.Done():
			}
		})

		select {
		case f.ended <- ended{from: one, about: about, err: err}:
		case <-f.quit:
		}
	})
}

// under is the path of the services watched.
func under(kind string) string {
	switch kind {
	case transportType:
		return transportPath
	case renderingType:
		return renderingPath
	default:
		return topologyPath
	}
}

// take reads one event and tells what it changes.
//
// What an event carries is never handed on as it came: the topology's
// events hold more than the groups, ThirdPartyMediaServersX among it,
// which carries the household's accounts with other services.
func (f *following) take(ctx context.Context, one told) error {
	switch one.about.kind {
	case topologyType:
		state, said := one.event.Properties.Get("ZoneGroupState")
		if !said {
			return nil
		}

		groups, err := topology(state)
		if err != nil {
			return err
		}

		f.regroup(ctx, groups)

		return nil
	case transportType:
		return f.played(one.about.id, one.event)
	default:
		return f.sounded(one.about.id, one.event)
	}
}

// regroup tells the groups when they changed, ends the subscriptions to
// speakers that are watched no more, and starts those to speakers that
// were not yet: the transport of each coordinator, the volume of each room.
func (f *following) regroup(ctx context.Context, groups []Group) {
	wanted := []subject{{host: f.system.seed, kind: topologyType}}

	for _, group := range groups {
		wanted = append(wanted, subject{id: group.Coordinator.ID, host: group.Coordinator.Host, kind: transportType})

		for _, member := range group.Members {
			wanted = append(wanted, subject{id: member.ID, host: member.Host, kind: renderingType})
		}
	}

	for about, one := range f.held {
		if slices.Contains(wanted, about) {
			continue
		}

		one.end()
		delete(f.held, about)

		// What it said is forgotten with it: watched again, it tells how
		// it then stands.
		if about.kind == transportType {
			delete(f.playing, about.id)
		} else {
			delete(f.sounding, about.id)
		}
	}

	// The groups first: what a group plays is told of a group already
	// told.
	if !f.regrouped || !slices.EqualFunc(groups, f.groups, same) {
		f.groups, f.regrouped = groups, true

		f.handle(Regrouped{Groups: copied(groups)})
	}

	for _, about := range wanted {
		if _, held := f.held[about]; !held {
			f.subscribe(ctx, about)
		}
	}
}

// same says whether two groups are one, with the same rooms.
func same(one, other Group) bool {
	return one.ID == other.ID && one.Coordinator == other.Coordinator && slices.Equal(one.Members, other.Members)
}

// copied is the groups as the handler's own.
func copied(groups []Group) []Group {
	own := make([]Group, 0, len(groups))

	for _, group := range groups {
		group.Members = slices.Clone(group.Members)
		own = append(own, group)
	}

	return own
}

// played reads a coordinator's transport event, and tells what the group
// plays when the event changes it, or is the first.
func (f *following) played(coordinator ID, event upnp.Event) error {
	values, err := changed(event)
	if err != nil {
		return err
	}

	before, known := f.playing[coordinator]
	status := before

	if state, said := values["TransportState"]; said {
		status.State = State(state)
	}

	if uri, said := values["CurrentTrackURI"]; said {
		status.URI = uri
	}

	if duration, said := values["CurrentTrackDuration"]; said {
		status.Duration = clock(duration)
	}

	if document, said := values["CurrentTrackMetaData"]; said {
		status.Title, status.Artist, status.Album = described(document)
	}

	if known && status == before {
		return nil
	}

	f.playing[coordinator] = status

	f.handle(Played{Coordinator: coordinator, Status: status})

	return nil
}

// sounded reads a room's rendering event, and tells how loud the room is
// when the event changes it, or is the first.
func (f *following) sounded(player ID, event upnp.Event) error {
	values, err := changed(event)
	if err != nil {
		return err
	}

	before, known := f.sounding[player]
	sound := before
	sound.Player = player

	if volume, said := values["Volume"]; said {
		level, err := strconv.Atoi(volume)
		if err != nil {
			return fmt.Errorf("%w: a volume of %q", ErrAnswer, volume)
		}

		sound.Volume = level
	}

	if mute, said := values["Mute"]; said {
		sound.Muted = mute == "1"
	}

	if known && sound == before {
		return nil
	}

	f.sounding[player] = sound

	f.handle(sound)

	return nil
}

// The document a transport or a rendering event carries in its
// LastChange: the variables that changed, each an element named after it,
// under the one instance there is.
type (
	lastChange struct {
		Instances []changedInstance `xml:"InstanceID"`
	}
	changedInstance struct {
		Variables []changedVariable `xml:",any"`
	}
	changedVariable struct {
		XMLName xml.Name `xml:""`
		// Value is what the variable now holds. A speaker has been
		// reported to write it as the element's text at times, which is
		// read when the attribute is not there.
		Value   *string `xml:"val,attr"`
		Text    string  `xml:",chardata"`
		Channel string  `xml:"channel,attr"`
	}
)

// changed is the variables an event's LastChange carries, by name: those
// of the speaker as a whole, the channel Master where a variable has one
// a channel. An event with no LastChange changes none.
func changed(event upnp.Event) (map[string]string, error) {
	values := make(map[string]string)

	written, said := event.Properties.Get("LastChange")
	if !said {
		return values, nil
	}

	var change lastChange
	if err := xml.Unmarshal([]byte(written), &change); err != nil {
		return nil, fmt.Errorf("%w: the LastChange: %w", ErrAnswer, err)
	}

	for _, instance := range change.Instances {
		for _, variable := range instance.Variables {
			if variable.Channel != "" && variable.Channel != "Master" {
				continue
			}

			if variable.Value != nil {
				values[variable.XMLName.Local] = *variable.Value
			} else {
				values[variable.XMLName.Local] = variable.Text
			}
		}
	}

	return values, nil
}
