// The background host service. On Windows it is linked as a GUI-subsystem
// program, so Task Scheduler starts it without a console window. All output
// goes to a bounded log file in the data directory.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"

	"tidalbridge/apps/dashboard"
	host "tidalbridge/apps/host-daemon"
	"tidalbridge/packages/config"
	historystore "tidalbridge/packages/history-store"
)

func main() {
	dir := flag.String("data-dir", config.DataDir(), "Configuration/state directory")
	flag.Parse()
	if err := os.MkdirAll(*dir, 0700); err != nil {
		os.Exit(1)
	}
	log := &historystore.RollingWriter{Path: filepath.Join(*dir, "service.log"), Limit: 4 << 20}
	defer log.Close()
	if crash, err := os.OpenFile(filepath.Join(*dir, "service.crash.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		debug.SetCrashOutput(crash, debug.CrashOptions{})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	err := host.Serve(ctx, *dir, dashboard.Handler(), log)
	if errors.Is(err, host.ErrAlreadyRunning) {
		fmt.Fprintln(log, "host already running; this instance exits")
		return
	}
	if err != nil {
		fmt.Fprintln(log, "host stopped:", err)
		os.Exit(1)
	}
}
