// Command fastr runs the local network file transfer server and its interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Nerow75/fastr/internal/app"
	"github.com/Nerow75/fastr/internal/assets"
	"github.com/Nerow75/fastr/internal/config"
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
	defer db.Close()

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

	// The pipe joins a fetching receiver to a supplying sender. When a receiver
	// starts waiting, the sender is told which item and from which offset, so a
	// page that reconnects mid-transfer knows where to resume.
	pipes := transfer.NewPipes()
	transfers := &app.Transfers{
		Store:    db,
		Pipes:    pipes,
		Notify:   httpapi.NewNotifier(events),
		Space:    transfer.PlatformChecker{Platform: plat},
		Log:      log,
		ReceiveD: settings.ReceiveFolder,
		StagingD: settings.StagingFolder,
	}
	pipes.OnDemand = func(key transfer.Key, offset uint64) {
		events.Publish(httpapi.Event{
			Type:       httpapi.EventTransferProgress,
			TransferID: key.TransferID,
			Payload:    map[string]any{"supply": key.ItemIndex, "offset": offset},
		})
	}

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
		DeviceID:   deviceIdentity(db, log),
		// Trusted mode has its own listener, which does not exist yet. Until
		// it does, no request is trusted, and a device that requires trusted
		// mode is refused with an explanation rather than quietly downgraded.
		Trusted: func(*http.Request) bool { return false },
	})

	server := httpapi.New(httpapi.Options{Logger: log, Bundle: bundle, Router: router})

	// FR-001: nothing has listened until this line.
	if err := server.Start(settings.BoundInterfaces, portFor(settings, f)); err != nil {
		return err
	}

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

	return waitForShutdown(server, log)
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

// deviceIdentity returns this instance's stable identifier, minting one on
// first run.
//
// It is stable across restarts and across address changes, which is what lets
// a phone recognise the same computer after the router hands it a new address.
func deviceIdentity(db *store.Store, log *slog.Logger) string {
	const key = "self"

	if dev, err := db.Device(key); err == nil && dev.ID != "" {
		return dev.ID
	}

	id := store.NewID().String()
	if err := db.PutDevice(store.Device{
		ID:   key,
		Name: id,
		Kind: store.KindComputer,
	}); err != nil {
		log.Error("could not persist device identity", "error", err)
	}
	return id
}
