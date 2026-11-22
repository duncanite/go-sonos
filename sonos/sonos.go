// Package sonos controls Sonos speakers on the LAN, over the UPnP services
// every speaker serves on port 1400: which speakers there are and how they
// are grouped, what a group is playing, play and pause, volume, moving a
// speaker in and out of a group, and all of it told as it changes. No
// account and no cloud.
//
// It opens no socket to find a speaker and reads no file: Find goes
// through a Browser its caller lends, every request through the caller's
// client, and events arrive at a listener opened where the caller says.
//
// Sonos does not document these services and calls the protocol
// unsupported. The contract here is the community's documentation,
// https://sonos.svrooij.io, generated from the speakers' own service
// descriptions; docs/design.md says what was read there.
package sonos

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	upnp "github.com/forkcloser/go-upnp"
)

// Port is the port a speaker serves its services on.
const Port = "1400"

// The services called, each by its type and the path its control and
// event URLs are under.
const (
	transportType = "urn:schemas-upnp-org:service:AVTransport:1"
	transportPath = "/MediaRenderer/AVTransport"

	renderingType = "urn:schemas-upnp-org:service:RenderingControl:1"
	renderingPath = "/MediaRenderer/RenderingControl"

	groupRenderingType = "urn:schemas-upnp-org:service:GroupRenderingControl:1"
	groupRenderingPath = "/MediaRenderer/GroupRenderingControl"

	topologyType = "urn:schemas-upnp-org:service:ZoneGroupTopology:1"
	topologyPath = "/ZoneGroupTopology"

	propertiesType = "urn:schemas-upnp-org:service:DeviceProperties:1"
	propertiesPath = "/DeviceProperties"
)

// The errors, to test with errors.Is.
var (
	// ErrRefused: the speaker said no. errors.As gives a *upnp.FaultError
	// with the UPnP code when it said why, a *upnp.StatusError otherwise.
	ErrRefused = upnp.ErrRefused
	// ErrForbidden: the speaker answered 403, which is an ErrRefused too.
	// Home Assistant's page gives that as the answer of a system with the
	// UPnP setting off in the Sonos app; docs/design.md has the source.
	ErrForbidden = errors.New("the speaker forbids UPnP control")
	// ErrAnswer: what the speaker answered, or told in an event, is not in
	// the documented shape, or is larger than it may be.
	ErrAnswer = upnp.ErrAnswer
	// ErrArgument: a value that cannot be sent; nothing was.
	ErrArgument = errors.New("not a valid argument")
	// ErrLost: a subscription can no longer be trusted to have told
	// everything, or the events listener ended by itself. Watching again
	// starts from how things stand.
	ErrLost = upnp.ErrLost
	// ErrClosed: the events listener was closed.
	ErrClosed = errors.New("the events listener is closed")
)

// Doer does an HTTP request: an *http.Client is one. It has to follow no
// redirect: a speaker's would send a request wherever the speaker points.
type Doer interface {
	// Do sends one request and gives its answer, as an *http.Client does.
	Do(request *http.Request) (*http.Response, error)
}

// ID is who a speaker is, "RINCON_...": the same wherever it is on the
// network and whatever room it is named. It is what the household calls
// the speaker, and the speaker's UDN without its "uuid:".
type ID string

// instance is the argument every transport and rendering action starts
// with: the documentation gives 0 as its only value.
func instance() upnp.Argument { return upnp.Argument{Name: "InstanceID", Value: "0"} }

// master is the channel a volume or a mute is read and set on: the
// speaker as a whole.
func master() upnp.Argument { return upnp.Argument{Name: "Channel", Value: "Master"} }

// at is the service of that type on the speaker at host, at the URLs the
// documentation gives for every speaker.
func at(host, kind, path string) upnp.Service {
	return upnp.Service{
		Type:    kind,
		Control: "http://" + host + path + "/Control",
		Events:  "http://" + host + path + "/Event",
	}
}

// call invokes an action of a service of the speaker at host.
func call(
	ctx context.Context, client Doer, host, kind, path, action string, args ...upnp.Argument,
) (upnp.Arguments, error) {
	answer, err := at(host, kind, path).Call(ctx, client, action, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", host, own(err))
	}

	return answer, nil
}

// own words an error of the UPnP library as this package's: what could not
// be said is an ErrArgument, and a 403 an ErrForbidden.
func own(err error) error {
	var status *upnp.StatusError

	switch {
	case errors.Is(err, upnp.ErrInvalid):
		return fmt.Errorf("%w: %w", ErrArgument, err)
	case errors.As(err, &status) && status.Status == http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrForbidden, err)
	default:
		return err
	}
}

// address is the host and port of the speaker a target names: the URL of
// its description, as a Found gives it, or a host, with the speakers' port
// when it carries none.
func address(target string) (string, error) {
	host := target

	if strings.Contains(target, "://") {
		location, err := url.Parse(target)
		if err != nil || location.Scheme != "http" || location.Host == "" {
			return "", fmt.Errorf("%w: %q is not the http URL of a speaker", ErrArgument, target)
		}

		host = location.Host
	}

	if host == "" {
		return "", fmt.Errorf("%w: an empty host", ErrArgument)
	}

	if _, _, err := net.SplitHostPort(host); err == nil {
		return host, nil
	}

	return net.JoinHostPort(strings.Trim(host, "[]"), Port), nil
}
