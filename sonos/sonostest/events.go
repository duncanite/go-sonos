package sonostest

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// GENA's methods.
	subscribe   = "SUBSCRIBE"
	unsubscribe = "UNSUBSCRIBE"

	// maxPosts is how many events may wait to be sent, and sendTime how
	// long a subscriber has to take one.
	maxPosts = 1024
	sendTime = 5 * time.Second

	// The namespaces of a LastChange, the transport's and the volume's.
	transportSpace = "urn:schemas-upnp-org:metadata-1-0/AVT/"
	renderingSpace = "urn:schemas-upnp-org:metadata-1-0/RCS/"
)

// subscriber is one subscription a speaker holds.
type subscriber struct {
	service  string
	sid      string
	callback string
	// next is the count its next event carries.
	next uint32
}

// delivery is one event on its way.
type delivery struct {
	callback string
	sid      string
	seq      uint32
	body     string
}

// Publish has every speaker tell every subscriber how things stand: what a
// test does after it set the household's state by hand.
func (h *Household) Publish() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, speaker := range h.Speakers {
		for _, service := range []string{Topology, Transport, Rendering} {
			speaker.tell(service, speaker.standing(service)...)
		}
	}
}

// Event has the speaker send its subscribers of a service an event of
// these properties, each a name and a value, whatever they are.
func (s *Speaker) Event(service string, properties ...[2]string) {
	s.house.mu.Lock()
	defer s.house.mu.Unlock()

	s.tell(service, properties...)
}

// Skip moves the count of the speaker's subscribers of a service on by one
// with no event sent: an event that did not arrive.
func (s *Speaker) Skip(service string) {
	s.house.mu.Lock()
	defer s.house.mu.Unlock()

	for _, one := range s.subscribers {
		if one.service == service {
			one.next++
		}
	}
}

// Subscribers is how many subscriptions to a service the speaker holds.
func (s *Speaker) Subscribers(service string) int {
	s.house.mu.Lock()
	defer s.house.mu.Unlock()

	held := 0

	for _, one := range s.subscribers {
		if one.service == service {
			held++
		}
	}

	return held
}

// Subscribed is every request about a subscription the speaker was sent,
// in order: "SUBSCRIBE AVTransport", "RENEW AVTransport", "UNSUBSCRIBE
// AVTransport".
func (s *Speaker) Subscribed() []string {
	s.house.mu.Lock()
	defer s.house.mu.Unlock()

	return append([]string(nil), s.requests...)
}

// subscription answers SUBSCRIBE and UNSUBSCRIBE. Called with the lock
// held.
func (s *Speaker) subscription(writer http.ResponseWriter, req *http.Request) {
	service := served(req.URL.Path, "/Event")
	sid := req.Header.Get("Sid")
	callback := req.Header.Get("Callback")

	switch {
	case s.house.Forbid:
		writer.WriteHeader(http.StatusForbidden)
	case req.Method == unsubscribe:
		s.requests = append(s.requests, "UNSUBSCRIBE "+service)

		if !s.drop(sid) {
			writer.WriteHeader(http.StatusPreconditionFailed)
		}
	case sid != "":
		s.requests = append(s.requests, "RENEW "+service)

		if !s.holds(sid) {
			writer.WriteHeader(http.StatusPreconditionFailed)

			return
		}

		writer.Header().Set("Sid", sid)
		writer.Header().Set("Timeout", s.house.Lease)
	case s.house.Deaf:
		s.requests = append(s.requests, "SUBSCRIBE "+service)

		writer.WriteHeader(http.StatusServiceUnavailable)
	case req.Header.Get("Nt") != "upnp:event" || !strings.HasPrefix(callback, "<") || !strings.HasSuffix(callback, ">"),
		service != Topology && service != Transport && service != Rendering:
		writer.WriteHeader(http.StatusPreconditionFailed)
	default:
		s.requests = append(s.requests, "SUBSCRIBE "+service)
		s.house.issued++

		created := &subscriber{
			service:  service,
			sid:      "uuid:" + s.UUID + "_sub" + strconv.Itoa(s.house.issued),
			callback: strings.Trim(callback, "<>"),
		}
		s.subscribers = append(s.subscribers, created)

		writer.Header().Set("Sid", created.sid)
		writer.Header().Set("Timeout", s.house.Lease)

		// The first event tells how things stand, under the count 0.
		s.send(created, s.standing(service))
	}
}

// holds says whether a subscription is held. Called with the lock held.
func (s *Speaker) holds(sid string) bool {
	for _, one := range s.subscribers {
		if one.sid == sid {
			return true
		}
	}

	return false
}

// drop ends a subscription, and says whether it was held. Called with the
// lock held.
func (s *Speaker) drop(sid string) bool {
	for index, one := range s.subscribers {
		if one.sid == sid {
			s.subscribers = append(s.subscribers[:index], s.subscribers[index+1:]...)

			return true
		}
	}

	return false
}

// tell sends the speaker's subscribers of a service an event. Called with
// the lock held.
func (s *Speaker) tell(service string, properties ...[2]string) {
	for _, one := range s.subscribers {
		if one.service == service {
			s.send(one, properties)
		}
	}
}

// send puts one event on its way, under the subscription's count. Called
// with the lock held.
func (s *Speaker) send(whom *subscriber, properties [][2]string) {
	if s.house.Silent {
		return
	}

	var body strings.Builder

	body.WriteString(`<?xml version="1.0"?><e:propertyset xmlns:e="urn:schemas-upnp-org:event-1-0">`)

	for _, property := range properties {
		fmt.Fprintf(
			&body,
			"<e:property><%s>%s</%s></e:property>",
			property[0],
			html.EscapeString(property[1]),
			property[0],
		)
	}

	body.WriteString(`</e:propertyset>`)

	post := delivery{callback: whom.callback, sid: whom.sid, seq: whom.next, body: body.String()}
	whom.next++

	select {
	case s.house.post <- post:
	case <-s.house.life.Done():
	}
}

// regrouped tells the subscribers of every speaker the groups, as any
// speaker knows them all. Called with the lock held.
func (h *Household) regrouped() {
	for _, speaker := range h.Speakers {
		speaker.tell(Topology, speaker.standing(Topology)...)
	}
}

// standing is everything a service of the speaker has to say: what a
// subscription's first event carries. Called with the lock held.
func (s *Speaker) standing(service string) [][2]string {
	switch service {
	case Topology:
		return [][2]string{{"ZoneGroupState", s.house.topology()}}
	case Transport:
		return lastChange(transportSpace,
			variable("TransportState", s.transportState()),
			variable("CurrentTrackURI", s.house.URI[s.UUID]),
			variable("CurrentTrackDuration", s.house.Duration[s.UUID]),
			variable("CurrentTrackMetaData", s.house.Track[s.UUID]),
			variable("TransportStatus", "OK"))
	default:
		return lastChange(renderingSpace, s.volume(), s.mute(), variable("Bass", "0"))
	}
}

// volume is the speaker's volume as an event writes it: the speaker as a
// whole, then each side of it, which is not the speaker's volume.
func (s *Speaker) volume() string {
	return channel("Volume", "Master", strconv.Itoa(s.Volume)) + channel("Volume", "LF", "100") +
		channel("Volume", "RF", "100")
}

// mute is the speaker's mute as an event writes it, the same way.
func (s *Speaker) mute() string {
	return channel("Mute", "Master", flag(s.Muted)) + channel("Mute", "LF", "0") + channel("Mute", "RF", "0")
}

// channel is one variable of a LastChange that comes once per channel.
func channel(name, of, value string) string {
	return "<" + name + ` channel="` + of + `" val="` + html.EscapeString(value) + `"/>`
}

// variable is one variable of a LastChange, its value in val.
func variable(name, value string) string {
	return "<" + name + ` val="` + html.EscapeString(value) + `"/>`
}

// lastChange is the one property of a transport or a volume event: the
// variables that changed, as a document under the one instance there is.
func lastChange(space string, variables ...string) [][2]string {
	return [][2]string{{
		"LastChange",
		`<Event xmlns="` + space + `"><InstanceID val="0">` + strings.Join(variables, "") + `</InstanceID></Event>`,
	}}
}

// deliver sends the events, one after the other in the order they were
// told, until the household is closed.
func (h *Household) deliver() {
	client := &http.Client{Timeout: sendTime}

	for {
		select {
		case <-h.life.Done():
			return
		case post := <-h.post:
			h.notify(client, post)
		}
	}
}

// notify sends one event; a subscriber that is not there is not told.
func (h *Household) notify(client *http.Client, post delivery) {
	sending, cancel := context.WithTimeout(h.life, sendTime)
	defer cancel()

	req, err := http.NewRequestWithContext(sending, "NOTIFY", post.callback, strings.NewReader(post.body))
	if err != nil {
		return
	}

	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header["NT"] = []string{"upnp:event"}
	req.Header["NTS"] = []string{"upnp:propchange"}
	req.Header["SID"] = []string{post.sid}
	req.Header["SEQ"] = []string{strconv.FormatUint(uint64(post.seq), 10)}

	answer, err := client.Do(req)
	if err != nil {
		return
	}

	_ = answer.Body.Close()
}
