// Command meow-server runs the Meow API: one process, one SQLite database.
//
// Configuration is read from the environment:
//
//	MEOW_ADDR           listen address (default :8080)
//	MEOW_DB             SQLite database path (default meow.db); keep it on a persistent volume
//	MEOW_RETENTION      how long resolved questions and notifications are kept (default 720h)
//	MEOW_TRUST_PROXY    "true" to take the client IP from X-Forwarded-For (behind a reverse proxy)
//	MEOW_MAX_KEYS_PER_DEVICE      active Device Keys per device (default 10, 0 = unlimited)
//	MEOW_MAX_PENDING_QUESTIONS    unanswered questions per device (default 20, 0 = unlimited)
//	MEOW_MAX_DAILY_MESSAGES       notifications and questions per device per 24h (default 1000, 0 = unlimited)
//	MEOW_INACTIVE_DEVICE_TTL      delete devices the app hasn't contacted for this long, e.g. 2160h (default off)
//	MEOW_LOG_LEVEL      debug | info | warn | error (default info)
//	MEOW_APNS_KEY_FILE  path to the APNs .p8 auth key; push is disabled when unset
//	MEOW_APNS_KEY_ID    APNs key ID
//	MEOW_APNS_TEAM_ID   Apple developer team ID
//	MEOW_APNS_TOPIC     iOS app bundle identifier
//
// "meow-server backup <path>" writes a consistent snapshot of the live database.
// "meow-server healthcheck" exits 0 when the local server answers /healthz.
// "meow-server check-apns" verifies the APNs credentials and exits.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"meow.blue/meow/server/internal/api"
	"meow.blue/meow/server/internal/push"
	"meow.blue/meow/server/internal/store"
)

var version = "dev"

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "backup":
		err = backup(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] == "check-apns":
		err = checkAPNsCommand()
	case len(os.Args) > 1 && os.Args[1] == "healthcheck":
		err = healthcheck()
	case len(os.Args) > 1 && os.Args[1] == "version":
		fmt.Println("meow-server", version)
	case len(os.Args) > 1:
		err = fmt.Errorf("unknown command %q (usage: meow-server [backup <path> | check-apns | healthcheck | version])", os.Args[1])
	default:
		err = run()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "meow-server:", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func newLogger() *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(env("MEOW_LOG_LEVEL", "info"))); err != nil {
		level = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func newPusher(log *slog.Logger) (push.Pusher, error) {
	keyFile := env("MEOW_APNS_KEY_FILE", "")
	if keyFile == "" {
		log.Warn("MEOW_APNS_KEY_FILE not set: notifications are stored but not pushed")
		return push.Disabled{Log: log}, nil
	}
	p8, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	return push.NewAPNs(p8, env("MEOW_APNS_KEY_ID", ""), env("MEOW_APNS_TEAM_ID", ""), env("MEOW_APNS_TOPIC", ""))
}

func run() error {
	log := newLogger()
	retention, err := time.ParseDuration(env("MEOW_RETENTION", "720h"))
	if err != nil || retention < time.Hour {
		return fmt.Errorf("MEOW_RETENTION must be a duration of at least 1h, e.g. 720h")
	}
	pusher, err := newPusher(log)
	if err != nil {
		return err
	}
	dbPath := env("MEOW_DB", "meow.db")
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	bg, stopBG := context.WithCancel(context.Background())
	defer stopBG()
	var wg sync.WaitGroup

	dispatcher := push.NewDispatcher(st, pusher, log)
	wg.Go(func() { dispatcher.Run(bg, 4) })

	const maxWait = 60 * time.Second
	if st.Limits, err = limitsFromEnv(); err != nil {
		return err
	}
	inactiveTTL, err := optionalDuration("MEOW_INACTIVE_DEVICE_TTL", 24*time.Hour)
	if err != nil {
		return err
	}
	log.Info("device limits", "keys", st.Limits.KeysPerDevice, "pending_questions", st.Limits.PendingQuestions,
		"daily_messages", st.Limits.DailyMessages, "inactive_device_ttl", inactiveTTL.String())

	srv := api.New(api.Config{
		Store: st, Push: dispatcher, Log: log, MaxWait: maxWait,
		TrustProxy: env("MEOW_TRUST_PROXY", "") == "true",
	})
	wg.Go(func() { maintain(bg, srv, st, retention, inactiveTTL, log) })
	// Check APNs in the background: a wrong Key ID shows up in the log at
	// deploy time instead of as silently missing notifications. It never
	// stops the server, since Apple may just be unreachable for a moment.
	wg.Go(func() { checkAPNs(bg, pusher, log) })

	hs := &http.Server{
		Addr:              env("MEOW_ADDR", ":8080"),
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      maxWait + 30*time.Second, // must outlast long polls
		IdleTimeout:       2 * time.Minute,
	}
	hs.RegisterOnShutdown(srv.Close) // release long polls so shutdown is quick

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("meow-server started", "version", version, "addr", hs.Addr, "db", dbPath)

	select {
	case err = <-errc:
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = hs.Shutdown(sctx)
		cancel()
	}
	stopBG()
	wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// maintain expires overdue questions every few seconds and prunes old data hourly.
func maintain(ctx context.Context, srv *api.Server, st *store.Store, retention, inactiveTTL time.Duration, log *slog.Logger) {
	expire := time.NewTicker(5 * time.Second)
	defer expire.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expire.C:
			if err := srv.ExpireDue(ctx); err != nil && ctx.Err() == nil {
				log.Error("expire questions", "err", err)
			}
		case <-prune.C:
			n, err := st.Prune(ctx, retention)
			if err != nil && ctx.Err() == nil {
				log.Error("prune", "err", err)
			} else if n > 0 {
				log.Info("pruned old records", "rows", n)
			}
			if inactiveTTL > 0 {
				n, err := st.PruneInactiveDevices(ctx, inactiveTTL)
				if err != nil && ctx.Err() == nil {
					log.Error("prune inactive devices", "err", err)
				} else if n > 0 {
					log.Info("deleted inactive devices", "devices", n)
				}
			}
		}
	}
}

func limitsFromEnv() (store.Limits, error) {
	var l store.Limits
	for _, v := range []struct {
		key string
		def int
		dst *int
	}{
		{"MEOW_MAX_KEYS_PER_DEVICE", 10, &l.KeysPerDevice},
		{"MEOW_MAX_PENDING_QUESTIONS", 20, &l.PendingQuestions},
		{"MEOW_MAX_DAILY_MESSAGES", 1000, &l.DailyMessages},
	} {
		n, err := strconv.Atoi(env(v.key, strconv.Itoa(v.def)))
		if err != nil || n < 0 {
			return l, fmt.Errorf("%s must be a whole number (0 = unlimited)", v.key)
		}
		*v.dst = n
	}
	return l, nil
}

// optionalDuration parses a duration that is off when unset or "0".
func optionalDuration(key string, min time.Duration) (time.Duration, error) {
	v := env(key, "0")
	if v == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < min {
		return 0, fmt.Errorf("%s must be a duration of at least %s, such as 2160h", key, min)
	}
	return d, nil
}

// healthcheck asks the server on MEOW_ADDR for /healthz. It lets a container
// image without curl define a health check.
// checkAPNs reports, per environment, whether Apple accepts the credentials.
// Development builds from Xcode use sandbox; TestFlight and the App Store
// use production.
func checkAPNs(ctx context.Context, p push.Pusher, log *slog.Logger) (ok bool) {
	a, isAPNs := p.(*push.APNs)
	if !isAPNs {
		return true
	}
	ok = true
	for _, env := range []struct {
		name    string
		sandbox bool
	}{{"sandbox", true}, {"production", false}} {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := a.Check(cctx, env.sandbox)
		cancel()
		if err != nil {
			ok = false
			log.Error("APNs credentials rejected", "environment", env.name, "err", err)
		} else {
			log.Info("APNs credentials accepted", "environment", env.name)
		}
	}
	return ok
}

func checkAPNsCommand() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	p, err := newPusher(log)
	if err != nil {
		return err
	}
	if _, isAPNs := p.(*push.APNs); !isAPNs {
		return errors.New("APNs is not configured (MEOW_APNS_KEY_FILE is unset)")
	}
	if !checkAPNs(context.Background(), p, log) {
		return errors.New("APNs check failed; see the errors above")
	}
	return nil
}

func healthcheck() error {
	addr := env("MEOW_ADDR", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}

func backup(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: meow-server backup <path>")
	}
	if _, err := os.Stat(args[0]); err == nil {
		return fmt.Errorf("%s already exists", args[0])
	}
	st, err := store.Open(env("MEOW_DB", "meow.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(context.Background(), args[0]); err != nil {
		return err
	}
	fmt.Println("backup written to", args[0])
	return nil
}
