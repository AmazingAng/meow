package main

import (
	"context"
	"log/slog"
	"time"
)

type gatewayMaintenance interface {
	PruneGatewayRoutes(context.Context) (int64, error)
}

func maintainGateway(ctx context.Context, st gatewayMaintenance, log *slog.Logger) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		if n, err := st.PruneGatewayRoutes(ctx); err != nil && ctx.Err() == nil {
			log.Error("prune gateway routes", "err", err)
		} else if n > 0 {
			log.Info("pruned inactive gateway routes", "routes", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
