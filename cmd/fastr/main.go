// Command fastr runs the local network file transfer server and its interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/assets"
	"github.com/Nerow75/fastr/internal/config"
	"github.com/Nerow75/fastr/internal/discovery"
	"github.com/Nerow75/fastr/internal/httpapi"
	"github.com/Nerow75/fastr/internal/i18n"
	"github.com/Nerow75/fastr/internal/pairing"
	"github.com/Nerow75/fastr/internal/platform"
	"github.com/Nerow75/fastr/internal/store"
	"github.com/Nerow75/fastr/internal/transfer"
)

// version is set at build time from the git description. See the Makefile.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, platform.ErrAlreadyRunning) {
			// Not a crash: the user launched it twice, which is easy to do
			// when it lives in the tray and has no window of its own.
			fmt.Fprintln(os.Stderr, "fastr is already running.")
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "fastr: %v\n", err)
		os.Exit(1)
	}
}

type flags struct {
	background bool
	verbose    bool
	port       int
	stateDir   string
	showVer    bool
}

func run(args []string) error {
	var f flags
	fs := flag.NewFlagSet("fastr", flag.ContinueOnError)
	fs.BoolVar(&f.background, "background", false, "start without opening the interface")
	fs.BoolVar(&f.verbose, "verbose", false, "log every request")
	fs.IntVar(&f.port, "port", 0, "listening port, 0 to let the system choose")
	fs.StringVar(&f.stateDir, "state-dir", "", "override the data directory")
	fs.BoolVar(&f.showVer, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if f.showVer {
		fmt.Println(version)
		return nil
	}

	level := slog.LevelInfo
	if f.verbose {
		level = slog.LevelDebug
	}
	log, scrubber := app.NewLogger(os.Stderr, level)

	plat := platform.Current()

	// Settings before anything else: they name the directories everything
	// below lives in.
	settings, err := loadSettings(plat, f)
	if err != nil {
		return err
	}
	if err := settings.EnsureFolders(); err != nil {
		return err
	}

	dataDir, err := plat.DataDir()
	if err != nil {
		return err
	}
	if f.stateDir != "" {
		dataDir = f.stateDir
	}

	// FR-050: exactly one instance owns the port and the store. Taking the
	// lock first means the second launch says so plainly rather than failing
	// later with a file-lock timeout nobody can interpret.
	lock, err := platform.AcquireInstanceLock(dataDir)
	if err != nil {
		return err
	}
	defer lock.Release()

	db, err := store.Open(filepath.Join(dataDir, "fastr.db"))
	if err != nil {
		return err
	}
	// Logged rather than ignored: bbolt flushes on close, so a failure here can
	// mean state that was written but did not land.
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("close store", "error", err)
		}
	}()

	bundle, err := assets.FS()
	if err != nil {
		return err
	}

	catalogues, err := i18n.Load()
	if err != nil {
		return err
	}
	log.Debug("catalogues loaded", "languages", catalogues.Languages())

	codes := pairing.NewCodes()
	// A live pairing code is registered with the log scrubber for exactly as
	// long as it is sensitive, so a code smuggled into a formatted message is
	// redacted rather than printed. FR-019.
	codes.SetScrubHooks(scrubber.Add, scrubber.Remove)

	events := httpapi.NewEvents()
	tickets := httpapi.NewTickets()
	sessions := pairing.NewSessions(db)
	pendings := pairing.NewPendings()

	// This instance's own identifier. A phone stores it when it pairs and names
	// it as the target of a transfer, so it must outlive a restart.
	selfID, err := db.SelfDevice(settings.DeviceName, string(plat.OS()))
	if err != nil {
		return err
	}

	// The pipe joins a fetching receiver to a supplying sender. When a receiver
	// starts waiting, the sender is told which item and from which offset, so a
	// page that reconnects mid-transfer knows where to resume.
	pipes := transfer.NewPipes()
	sinks := transfer.NewSinks()
	// Partial data stays on disk for a later resume, but its file handle must
	// not: Windows will not let the retention sweep remove a file that is still
	// open.
	defer sinks.CloseAll()

	transfers := &app.Transfers{
		Store:    db,
		Pipes:    pipes,
		Sinks:    sinks,
		Notify:   httpapi.NewNotifier(events),
		Space:    transfer.PlatformChecker{Platform: plat},
		Log:      log,
		SelfID:   selfID,
		Rules:    platform.Rules(),
		ReceiveD: settings.ReceiveFolder,
		StagingD: settings.StagingFolder,
		// FR-036: a transfer that lands while every page is closed has no other
		// way to be noticed.
		Announce: &app.DesktopAnnouncer{
			Bundle:   catalogues,
			Language: settings.Language,
			Log:      log,
		},
	}
	// Before anything can ask for the queue: a transfer the previous run died
	// holding would otherwise sit in the single active slot with nobody driving
	// it, blocking every transfer behind it. FR-035e.
	if _, err := transfers.Recover(); err != nil {
		return err
	}

	pipes.OnDemand = func(key transfer.Key, offset uint64) {
		events.Publish(httpapi.Event{
			Type:       httpapi.EventTransferProgress,
			TransferID: key.TransferID,
			Payload:    map[string]any{"supply": key.ItemIndex, "offset": offset},
		})
	}

	// Discovery. The browser is created before the router because the device
	// list reads it, and started after the listeners because there is no point
	// announcing a machine that is not yet answering.
	browser := discovery.NewBrowser(selfID, log)
	prober := discovery.NewProber()
	browser.OnChange(func() {
		// The whole list moved; each page reads it back scoped to itself.
		// FR-008: no manual refresh.
		events.Publish(httpapi.Event{Type: httpapi.EventDiscoveryChanged})
	})

	// Declared before the router and assigned after Start, because the
	// invitation endpoint has to report addresses that do not exist until the
	// listeners are bound. The closure reads it at call time, never at build
	// time.
	var server *httpapi.Server

	router := httpapi.NewRouter(httpapi.Deps{
		Log:        log,
		Store:      db,
		Sessions:   sessions,
		Codes:      codes,
		Handshakes: pairing.NewHandshakes(),
		Pendings:   pendings,
		Bundle:     bundle,
		Events:     events,
		Tickets:    tickets,
		Transfers:  transfers,
		DeviceName: settings.DeviceName,
		DeviceID:   selfID,
		Addresses: func() []string {
			if server == nil {
				return nil
			}
			return server.Addresses()
		},
		// Trusted mode has its own listener, which does not exist yet. Until
		// it does, no request is trusted, and a device that requires trusted
		// mode is refused with an explanation rather than quietly downgraded.
		Trusted:   func(*http.Request) bool { return false },
		Discovery: browser,
		Browser:   browser,
		Prober:    prober,
	})

	server = httpapi.New(httpapi.Options{Logger: log, Bundle: bundle, Router: router})

	// FR-001: nothing has listened until this line.
	if err := server.Start(settings.BoundInterfaces, portFor(settings, f)); err != nil {
		return err
	}

	// Discovery starts only now. FR-001 makes listening an explicit act, and
	// announcing this machine's name to everyone on the network is exactly the
	// kind of thing that must not happen before the user asks for it.
	discovering, stopDiscovery := context.WithCancel(context.Background())
	defer stopDiscovery()
	startDiscovery(discovering, server, browser, prober, settings.DeviceName, selfID, plat, log)

	addresses := server.Addresses()
	fmt.Printf("fastr %s is listening.\n", version)
	for _, addr := range addresses {
		fmt.Printf("  http://%s\n", addr)
	}
	fmt.Printf("Receiving into %s\n", settings.ReceiveFolder)

	// A pairing code is displayed from the start, so a phone that scans
	// immediately has something to enter. FR-002.
	code, err := codes.Issue()
	if err != nil {
		return err
	}
	fmt.Printf("\nPairing code: %s\n", code.Display())
	fmt.Println("It is valid for three minutes and can be used once.")
	fmt.Println("Entering it puts the device in a queue; you approve it here.")

	if !f.background {
		fmt.Println("\nOpen one of the addresses above to use fastr.")
	}

	// The retention sweep, at startup and daily thereafter, per data-model.md.
	// Started after the listeners so a page opened immediately is subscribed in
	// time to hear what the first sweep removed.
	sweeping := startSweeper(transfers, log)
	defer sweeping()

	// FR-016d: a transfer waiting on a human is refused rather than queued
	// indefinitely. Checked often, because the window is two minutes and a
	// sender watching a spinner deserves an answer close to when it expires.
	expiring := startAcceptanceExpiry(transfers, log)
	defer expiring()

	return waitForShutdown(server, log)
}

// startDiscovery advertises this instance and begins looking for others.
//
// Nothing here can stop the application from working. A network that blocks
// multicast is ordinary — offices and guest wifi both do it — and the answer is
// the manual address field, not a refusal to run: a user who only ever sends
// from their phone would otherwise be blocked by a feature they never use.
// Every failure is therefore logged and reported through the device list rather
// than returned.
func startDiscovery(
	ctx context.Context,
	server *httpapi.Server,
	browser *discovery.Browser,
	prober *discovery.Prober,
	deviceName, selfID string,
	plat platform.Platform,
	log *slog.Logger,
) {
	addresses := server.Addresses()
	ips, port := advertisableAddresses(addresses)

	if port == 0 {
		log.Info("not advertising: no local address to publish")
	} else {
		published, err := discovery.Advertise(discovery.Advertisement{
			DeviceID:  selfID,
			Name:      deviceName,
			OS:        string(plat.OS()),
			Port:      port,
			Addresses: ips,
			Log:       log,
		})
		if err != nil {
			log.Info("not advertising on this network; other computers will need the address typed in",
				"error", err)
		} else {
			context.AfterFunc(ctx, func() { _ = published.Close() })
		}
	}

	browser.Start(ctx)
	// Reachability on a slower beat than browsing. A record arriving is news;
	// a device that answered thirty seconds ago almost certainly still answers,
	// and probing every machine on the network more often than that is a worse
	// neighbour than the staleness it removes.
	go prober.Watch(ctx, browser, 30*time.Second)
}

// advertisableAddresses picks what to publish from what the server bound.
//
// Loopback is dropped: a record telling other machines to connect to 127.0.0.1
// points every one of them at itself. If loopback is all there is, there is
// nothing to advertise and saying so beats publishing a record that cannot
// work.
func advertisableAddresses(bound []string) ([]net.IP, int) {
	var ips []net.IP
	port := 0

	for _, address := range bound {
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		if number, err := strconv.Atoi(portText); err == nil {
			port = number
		}
		ips = append(ips, ip)
	}
	return ips, port
}

// startAcceptanceExpiry refuses transfers nobody answered, and returns a
// function that stops it.
func startAcceptanceExpiry(transfers *app.Transfers, log *slog.Logger) func() {
	done := make(chan struct{})

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if expired := transfers.ExpireAcceptances(time.Now()); expired > 0 {
					log.Info("refused transfers nobody answered", "count", expired)
				}
			case <-done:
				return
			}
		}
	}()

	return func() { close(done) }
}

// startSweeper runs the retention sweep now and once a day, and returns a
// function that stops it.
//
// Daily rather than on a timer tied to the windows themselves: everything it
// removes has been idle for a week or longer, so the difference between
// sweeping at the exact hour and sweeping within a day of it is not something a
// user could notice, and a fixed interval has no edge cases.
func startSweeper(transfers *app.Transfers, log *slog.Logger) func() {
	sweep := func() {
		if _, err := transfers.Sweep(time.Now()); err != nil {
			log.Error("retention sweep", "error", err)
		}
	}
	sweep()

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sweep()
			case <-done:
				return
			}
		}
	}()

	return func() { close(done) }
}

// waitForShutdown blocks until the user stops the process, then releases the
// listeners. FR-050: stopping is one action and it actually stops.
func waitForShutdown(server *httpapi.Server, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	fmt.Println("\nStopping.")

	if err := server.Stop(context.Background()); err != nil {
		log.Error("shutdown", "error", err)
		return err
	}
	return nil
}

// loadSettings reads settings, writing defaults on first run.
func loadSettings(plat platform.Platform, f flags) (config.Settings, error) {
	st, err := config.NewStore(plat)
	if err != nil {
		return config.Settings{}, err
	}
	return st.LoadOrInit(plat)
}

// portFor lets the flag win over the stored setting, so a second instance can
// be run on another port for testing without editing the file.
func portFor(settings config.Settings, f flags) int {
	if f.port != 0 {
		return f.port
	}
	return settings.Port
}
