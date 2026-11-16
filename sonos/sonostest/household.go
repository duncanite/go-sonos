// Package sonostest is a Sonos household for tests: one HTTP server per
// speaker, each answering the UPnP actions this module calls, as the
// community's documentation of the services gives them, over a state the
// test reads and sets. It records what each speaker was asked.
//
// Three things here are not from that documentation. What a speaker
// answers a transport action when it does not coordinate its group: the
// fake refuses with a UPnP fault, so a test that sends one to the wrong
// speaker fails. How strict a speaker is: the fake refuses a request whose
// SOAPAction header, instance or channel is not the documented one, so a
// test fails on a request a speaker might still take. And the 403 of
// Forbid, which is Home Assistant's account of a system with UPnP off.
package sonostest

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

const (
	maxBody = 1 << 20
	// faultNotCoordinator is the fake's own refusal; see the package
	// documentation.
	faultNotCoordinator = 800
	faultInvalidAction  = 401
	faultInvalidArgs    = 402
)

// Speaker is one speaker of the household.
type Speaker struct {
	*httptest.Server

	// UUID and Name are what the zone group state gives for it.
	UUID string
	Name string
	// Invisible marks a speaker a person does not see as a room: the
	// second of a pair, a sub, a bridge.
	Invisible bool
	// Volume and Muted are its own.
	Volume int
	Muted  bool
	// Group is the UUID of the coordinator of the group it is in; its own
	// when it plays alone.
	Group string
	// Calls is every action it received, "Service.Action", in order.
	Calls []string

	house *Household
}

// Household is the fake. A test sets its exported fields, and its
// speakers', between requests; the servers hold its lock around them.
type Household struct {
	mu sync.Mutex
	// Speakers is every speaker, in the order New was given their names.
	Speakers []*Speaker
	// State is each group's transport state, by its coordinator's UUID;
	// a group with none there is STOPPED.
	State map[string]string
	// Track is the DIDL-Lite document a coordinator reports for what it
	// plays, with URI, Position and Duration beside it, by UUID.
	Track    map[string]string
	URI      map[string]string
	Position map[string]string
	Duration map[string]string
	// Bare makes the topology answer with ZoneGroups as its root, as
	// older software does, and not wrapped in ZoneGroupState.
	Bare bool
	// Forbid makes every speaker answer 403, which Home Assistant's page
	// gives as the answer of a system with UPnP turned off.
	Forbid bool
	// Raw, when not nil, is the whole body every speaker answers with,
	// under a 200.
	Raw []byte
}

// New starts a household with one speaker per name, each playing alone at
// volume 20. The caller closes it.
func New(names ...string) *Household {
	house := &Household{
		State:    map[string]string{},
		Track:    map[string]string{},
		URI:      map[string]string{},
		Position: map[string]string{},
		Duration: map[string]string{},
	}

	for index, name := range names {
		speaker := &Speaker{
			UUID:   fmt.Sprintf("RINCON_%012d01400", index+1),
			Name:   name,
			Volume: 20,
			house:  house,
		}
		speaker.Group = speaker.UUID
		speaker.Server = httptest.NewServer(http.HandlerFunc(speaker.serve))
		house.Speakers = append(house.Speakers, speaker)
	}

	return house
}

// Close stops every speaker.
func (h *Household) Close() {
	for _, speaker := range h.Speakers {
		speaker.Close()
	}
}

// Host is the speaker's address, host and port.
func (s *Speaker) Host() string {
	return strings.TrimPrefix(s.URL, "http://")
}

// Received is the actions the speaker was sent so far.
func (s *Speaker) Received() []string {
	s.house.mu.Lock()
	defer s.house.mu.Unlock()

	return append([]string(nil), s.Calls...)
}

// A SOAP request as far as the fake reads it: the action's element and
// its arguments.
type (
	request struct {
		Body requestBody `xml:"Body"`
	}
	requestBody struct {
		Action requestAction `xml:",any"`
	}
	requestAction struct {
		XMLName xml.Name     `xml:""`
		Args    []requestArg `xml:",any"`
	}
	requestArg struct {
		XMLName xml.Name `xml:""`
		Value   string   `xml:",chardata"`
	}
)

func (s *Speaker) serve(writer http.ResponseWriter, req *http.Request) {
	s.house.mu.Lock()
	defer s.house.mu.Unlock()

	raw, _ := io.ReadAll(io.LimitReader(req.Body, maxBody))

	var asked request

	_ = xml.Unmarshal(raw, &asked)

	action := asked.Body.Action.XMLName.Local
	args := map[string]string{}

	for _, arg := range asked.Body.Action.Args {
		args[arg.XMLName.Local] = arg.Value
	}

	service := strings.TrimSuffix(strings.TrimPrefix(req.URL.Path, "/MediaRenderer"), "/Control")
	service = strings.Trim(service, "/")
	s.Calls = append(s.Calls, service+"."+action)

	writer.Header().Set("Content-Type", `text/xml; charset="utf-8"`)

	if s.house.Forbid {
		writer.WriteHeader(http.StatusForbidden)

		return
	}

	body := s.house.Raw

	if body == nil {
		answer, fault := s.asked(req.Header.Get("SoapAction"), service, action, args)
		if fault != 0 {
			writer.WriteHeader(http.StatusInternalServerError)
		}

		body = envelope(service, action, answer, fault)
	}

	// #nosec G705 -- a test's stand-in answering this module's own client in XML; no browser reads it
	_, _ = writer.Write(body)
}

// envelope is the SOAP answer: the action's response with its elements,
// or a fault carrying a UPnP error code.
func envelope(service, action string, answer [][2]string, fault int) []byte {
	var body bytes.Buffer

	body.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body>`)

	if fault != 0 {
		fmt.Fprintf(&body, `<s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring><detail>`+
			`<UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>%d</errorCode></UPnPError>`+
			`</detail></s:Fault>`, fault)
	} else {
		fmt.Fprintf(&body, `<u:%sResponse xmlns:u="urn:schemas-upnp-org:service:%s:1">`, action, service)

		for _, pair := range answer {
			fmt.Fprintf(&body, "<%s>%s</%s>", pair[0], html.EscapeString(pair[1]), pair[0])
		}

		fmt.Fprintf(&body, `</u:%sResponse>`, action)
	}

	body.WriteString(`</s:Body></s:Envelope>`)

	return body.Bytes()
}

// asked refuses a request that is not shaped as the documentation gives
// it, the SOAPAction header naming the service's type and the action, the
// instance 0 and the channel Master where the action takes one; act
// answers the others.
func (s *Speaker) asked(header, service, action string, args map[string]string) ([][2]string, int) {
	if header != `"urn:schemas-upnp-org:service:`+service+`:1#`+action+`"` {
		return nil, faultInvalidAction
	}

	instance, takesInstance := args["InstanceID"]
	channel, takesChannel := args["Channel"]

	switch {
	case service != "ZoneGroupTopology" && (!takesInstance || instance != "0"),
		takesChannel && channel != "Master",
		service == "RenderingControl" && !takesChannel,
		action == "Play" && args["Speed"] != "1":
		return nil, faultInvalidArgs
	}

	return s.act(service, action, args)
}

// act is the speaker's behaviour: the answer's elements in order, or a
// UPnP fault's code.
func (s *Speaker) act(service, action string, args map[string]string) ([][2]string, int) {
	house := s.house
	coordinates := s.Group == s.UUID

	switch service + "." + action {
	case "ZoneGroupTopology.GetZoneGroupState":
		return [][2]string{{"ZoneGroupState", house.topology()}}, 0
	case "AVTransport.GetTransportInfo":
		// The documentation: a speaker that does not coordinate always
		// answers PLAYING.
		state := "PLAYING"
		if coordinates {
			state = house.state(s.UUID)
		}

		return [][2]string{
			{"CurrentTransportState", state}, {"CurrentTransportStatus", "OK"}, {"CurrentSpeed", "1"},
		}, 0
	case "AVTransport.GetPositionInfo":
		return [][2]string{
			{"Track", "1"},
			{"TrackDuration", house.Duration[s.UUID]},
			{"TrackMetaData", house.Track[s.UUID]},
			{"TrackURI", house.URI[s.UUID]},
			{"RelTime", house.Position[s.UUID]},
		}, 0
	case "AVTransport.Play", "AVTransport.Pause", "AVTransport.Stop", "AVTransport.Next", "AVTransport.Previous":
		if !coordinates {
			return nil, faultNotCoordinator
		}

		switch action {
		case "Play":
			house.State[s.UUID] = "PLAYING"
		case "Pause":
			house.State[s.UUID] = "PAUSED_PLAYBACK"
		case "Stop":
			house.State[s.UUID] = "STOPPED"
		}

		return nil, 0
	case "AVTransport.SetAVTransportURI":
		coordinator, joins := strings.CutPrefix(args["CurrentURI"], "x-rincon:")
		if !joins || house.speaker(coordinator) == nil {
			return nil, faultInvalidArgs
		}

		s.Group = coordinator

		return nil, 0
	case "AVTransport.BecomeCoordinatorOfStandaloneGroup":
		s.Group = s.UUID

		return [][2]string{{"DelegatedGroupCoordinatorID", ""}, {"NewGroupID", s.UUID + ":1"}}, 0
	}

	return s.render(service, action, args)
}

// render is the volume side of act.
func (s *Speaker) render(service, action string, args map[string]string) ([][2]string, int) {
	switch service + "." + action {
	case "RenderingControl.GetVolume":
		return [][2]string{{"CurrentVolume", strconv.Itoa(s.Volume)}}, 0
	case "RenderingControl.SetVolume":
		level, err := strconv.Atoi(args["DesiredVolume"])
		if err != nil {
			return nil, faultInvalidArgs
		}

		s.Volume = level

		return nil, 0
	case "RenderingControl.GetMute":
		return [][2]string{{"CurrentMute", flag(s.Muted)}}, 0
	case "RenderingControl.SetMute":
		s.Muted = args["DesiredMute"] == "1"

		return nil, 0
	case "GroupRenderingControl.GetGroupVolume", "GroupRenderingControl.SnapshotGroupVolume",
		"GroupRenderingControl.SetGroupVolume":
		return s.renderGroup(action, args)
	}

	return nil, faultInvalidAction
}

// renderGroup is the group's volume, which only its coordinator answers:
// the mean of its members', and one level set on them all.
func (s *Speaker) renderGroup(action string, args map[string]string) ([][2]string, int) {
	if s.Group != s.UUID {
		return nil, faultNotCoordinator
	}

	members := s.house.members(s.UUID)

	switch action {
	case "GetGroupVolume":
		total := 0
		for _, member := range members {
			total += member.Volume
		}

		return [][2]string{{"CurrentVolume", strconv.Itoa(total / len(members))}}, 0
	case "SetGroupVolume":
		level, err := strconv.Atoi(args["DesiredVolume"])
		if err != nil {
			return nil, faultInvalidArgs
		}

		for _, member := range members {
			member.Volume = level
		}
	}

	return nil, 0
}

func flag(set bool) string {
	if set {
		return "1"
	}

	return "0"
}

func (h *Household) state(uuid string) string {
	if state, set := h.State[uuid]; set {
		return state
	}

	return "STOPPED"
}

func (h *Household) speaker(uuid string) *Speaker {
	for _, speaker := range h.Speakers {
		if speaker.UUID == uuid {
			return speaker
		}
	}

	return nil
}

func (h *Household) members(coordinator string) []*Speaker {
	var members []*Speaker

	for _, speaker := range h.Speakers {
		if speaker.Group == coordinator {
			members = append(members, speaker)
		}
	}

	return members
}

// topology is the zone group state as XML: a group per coordinator, its
// members in the household's order.
func (h *Household) topology() string {
	var groups strings.Builder

	for _, coordinator := range h.Speakers {
		members := h.members(coordinator.UUID)
		if coordinator.Group != coordinator.UUID || len(members) == 0 {
			continue
		}

		fmt.Fprintf(&groups, `<ZoneGroup Coordinator="%s" ID="%s:1">`, coordinator.UUID, coordinator.UUID)

		for _, member := range members {
			invisible := ""
			if member.Invisible {
				invisible = ` Invisible="1"`
			}

			fmt.Fprintf(
				&groups,
				`<ZoneGroupMember UUID="%s" Location="%s/xml/device_description.xml" ZoneName="%s"%s/>`,
				member.UUID,
				member.URL,
				html.EscapeString(member.Name),
				invisible,
			)
		}

		groups.WriteString(`</ZoneGroup>`)
	}

	if h.Bare {
		return `<ZoneGroups>` + groups.String() + `</ZoneGroups>`
	}

	return `<ZoneGroupState><ZoneGroups>` + groups.String() + `</ZoneGroups><VanishedDevices/></ZoneGroupState>`
}
