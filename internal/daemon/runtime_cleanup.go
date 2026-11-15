package daemon

import (
	"context"
	"errors"
	"time"
)

func restoreDNSWithinBudget(parent context.Context, budget, ownerReserve time.Duration, router, owner func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(parent, budget)
	defer cancel()
	// Both restorations keep one overall deadline. Only the first operation
	// has an earlier deadline, so a slow dnsmasq cannot consume the time
	// reserved for enabling and starting the previous DNS owner. Fast router
	// recovery leaves all of the remaining total budget available to it.
	deadline, _ := ctx.Deadline()
	routerCtx, routerCancel := context.WithDeadline(ctx, deadline.Add(-ownerReserve))
	routerErr := router(routerCtx)
	routerCancel()
	return errors.Join(routerErr, owner(ctx))
}

// Call after workers and the managed DNS listener have stopped. Restoring the
// previous resolver and closing the core are independent; both must finish
// before init can replace the binary or start another daemon.
func closeCoreAndRestoreDNS(closeCore, restoreDNS func() error) (err error) {
	if restoreDNS == nil {
		return closeCore()
	}
	restored := make(chan error, 1)
	go func() { restored <- restoreDNS() }()
	defer func() { err = errors.Join(err, <-restored) }()
	return closeCore()
}
