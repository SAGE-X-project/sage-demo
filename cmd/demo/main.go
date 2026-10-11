// SPDX-License-Identifier: LGPL-3.0-or-later

// Command demo runs the SAGE demo: the same transfer workload with and
// without SAGE protection, with the same injected faults.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sage-x-project/sage-demo/internal/demo"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	// After the first signal, a second one ends the process at once.
	context.AfterFunc(ctx, stop)
	code := demo.Main(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
