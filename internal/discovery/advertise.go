// Package discovery finds other fastr computers on the same network, and lets
// this one be found.
//
// It implements contracts/discovery.md: mDNS and DNS-SD advertising
// `_fastr._tcp`, with manual address entry as the documented fallback for
// networks that block multicast. There is no discovery mechanism of the
// project's own design, and nothing here reaches beyond the local link.
//
// Two rules run through the whole package.
//
// **The record is a hint, never an authority.** A service record can outlive
// the process that published it, and anything on the network can publish one.
// So a device is listed from its record but confirmed by a request to
// `/connect`, and nothing in it is ever an input to an access decision: pairing
// is what grants access, and pairing does not consult this package.
//
// **Nothing is removed for being quiet.** A record that stops being answered
// marks a device unreachable; it does not delete it. FR-004 scenario 3: a
// device must not vanish from under a selection in progress.
package discovery

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/hashicorp/mdns"
)

// ServiceType is the DNS-SD service this project advertises and browses.
const ServiceType = "_fastr._tcp"

// domain is the multicast DNS domain. Nothing here ever leaves the local link,
// which is a property of this constant as much as of the address checks.
const domain = "local."

// Advertisement is what this instance publishes about itself.
type Advertisement struct {
	// DeviceID is the stable identifier a peer stores when it pairs. It is the
	// authoritative identity; addresses are advisory.
	DeviceID string
	// Name is what the user called this machine.
	Name string
	// OS is advisory only, and never an input to an access decision.
	OS string
	// Port is the port the HTTP server actually bound to.
	Port int
	// Addresses are the local addresses to publish. Empty lets the library
	// choose, which is right on a machine with one interface and wrong on one
	// with several, so the caller passes what the server is really listening on.
	Addresses []net.IP
	// TLS reports whether trusted mode is initialized here.
	TLS bool
	// Log receives the mDNS library's own output. Nil discards it.
	Log *slog.Logger
}

// Version is the protocol version published in the TXT record, so two
// incompatible builds refuse each other with a clear message rather than
// failing obscurely halfway through a handshake.
const Version = 1

// Advertiser publishes this instance until it is closed.
type Advertiser struct {
	server *mdns.Server
}

// Advertise starts publishing.
//
// It is called when the user starts the server and never before: FR-001 makes
// listening an explicit act, and announcing a machine's name to the whole
// network is exactly the kind of thing that must not happen on its own.
func Advertise(a Advertisement) (*Advertiser, error) {
	if a.DeviceID == "" {
		return nil, fmt.Errorf("advertise: no device id")
	}
	if a.Port <= 0 || a.Port > 65535 {
		return nil, fmt.Errorf("advertise: port %d out of range", a.Port)
	}

	service, err := mdns.NewMDNSService(
		InstanceName(a.Name, a.DeviceID),
		ServiceType,
		domain,
		hostName(),
		a.Port,
		a.Addresses,
		TXTRecord(a),
	)
	if err != nil {
		return nil, fmt.Errorf("advertise: %w", err)
	}

	server, err := mdns.NewServer(&mdns.Config{Zone: service, Logger: libraryLogger(a.Log)})
	if err != nil {
		return nil, fmt.Errorf("advertise: %w", err)
	}
	return &Advertiser{server: server}, nil
}

// Close stops publishing. Safe on a nil advertiser, so a caller that degraded
// to no discovery at all does not have to remember which case it is in.
func (a *Advertiser) Close() error {
	if a == nil || a.server == nil {
		return nil
	}
	return a.server.Shutdown()
}

// InstanceName is the DNS-SD instance name: the device name with a short
// identifier after it.
//
// The identifier is not decoration. Two machines called "Laptop" are the
// ordinary case in a household, and FR-005 requires them to stay
// distinguishable; an instance name is also the DNS-SD uniqueness key, so
// without it the second one would collide with the first and be renamed by the
// stack into something the user never chose.
func InstanceName(name, deviceID string) string {
	if strings.TrimSpace(name) == "" {
		name = "fastr"
	}
	return fmt.Sprintf("%s (%s)", name, ShortID(deviceID))
}

// ShortID is the tail of a device identifier, which is what a person is shown.
//
// The tail rather than the head: a ULID begins with a timestamp, so two devices
// that first ran on the same day share their leading characters, and the part
// that actually distinguishes them is at the end.
func ShortID(deviceID string) string {
	const shown = 6
	if len(deviceID) <= shown {
		return deviceID
	}
	return deviceID[len(deviceID)-shown:]
}

// TXTRecord is what the service record carries, per contracts/discovery.md.
//
// No credential, no key, and nothing derived from user content. Every device on
// the network can read this, and it is written on that assumption: it holds
// exactly what a device list needs to draw a row before any pairing exists.
func TXTRecord(a Advertisement) []string {
	tls := "0"
	if a.TLS {
		tls = "1"
	}
	return []string{
		fmt.Sprintf("v=%d", Version),
		"id=" + a.DeviceID,
		"name=" + a.Name,
		"kind=computer", // only computers advertise; phones exist through a browser
		"os=" + a.OS,
		"tls=" + tls,
	}
}

// osHostname is os.Hostname, replaced in tests.
var osHostname = os.Hostname

// hostName is the name published in the service record.
//
// The system host name with the mDNS domain appended, which is what a DNS-SD
// responder is expected to publish. A machine with no usable host name falls
// back to something valid rather than refusing to advertise: the name is
// cosmetic here, because every address the record points at is published
// explicitly alongside it.
func hostName() string {
	host, err := osHostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "fastr"
	}
	return strings.TrimSuffix(host, ".") + "." + domain
}
