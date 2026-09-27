package main

import (
	"context"
	"ddyidc/internal/backup"
	"ddyidc/internal/cdnsell"
	"ddyidc/internal/certification"
	"ddyidc/internal/groupbuy"
	instancerepo "ddyidc/internal/instance/repository"
	instanceservice "ddyidc/internal/instance/service"
	marketingrepo "ddyidc/internal/marketing/repository"
	marketingservice "ddyidc/internal/marketing/service"
	"ddyidc/internal/marketplace"
	"ddyidc/internal/navigation"
	"ddyidc/internal/notify"
	paymentrepo "ddyidc/internal/payment/repository"
	"ddyidc/internal/points"
	productrepo "ddyidc/internal/product/repository"
	"ddyidc/internal/pushtransfer"
	"ddyidc/internal/shared/snowflake"
	ticketrepo "ddyidc/internal/ticket/repository"
	upstreamservice "ddyidc/internal/upstream/service"
	"ddyidc/internal/videobind"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/db"
	"fmt"
)

func validateMigrationSocket(offline bool, uid int, socket, host string) error {
	if !offline || uid != 0 || socket != "/var/run/mysqld/mysqld.sock" || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		return fmt.Errorf("migration socket is restricted to explicit local root offline migration")
	}
	return nil
}

// Only called by the offline migrator; no HTTP server, cron, provider request or
// development account bootstrap is started here. Existing settings are retained.
func seedModules(ctx context.Context, key []byte, root string) error {
	if err := snowflake.Prepare(ctx, db.DB); err != nil {
		return err
	}
	if err := snowflake.Configure(ctx, db.DB); err != nil {
		return err
	}
	upstream := upstreamservice.NewDefaultStore(key)
	instance := instanceservice.NewService(instancerepo.NewRepository(db.G), upstream)
	instance.SetManagement(db.G)
	backups := backup.New(db.G, key, root)
	steps := []struct {
		name string
		run  func() error
	}{
		{"upstream", upstream.EnsureSyncSchema},
		{"product", func() error { return productrepo.NewRepository(db.G).EnsureSpecsSchema(ctx) }},
		{"marketing", func() error {
			return marketingservice.NewService(db.G, marketingrepo.NewRepository(db.G)).EnsureDefaults(ctx)
		}},
		{"security", func() error {
			return auth.NewManager("migration-only-unused", "migration-only-unused", 900, 900).LoadSecurityConfig(ctx, db.G)
		}},
		{"notify", func() error { return notify.NewRepository(db.G).EnsureSchema(ctx) }},
		{"navigation", func() error { return navigation.New(db.G).EnsureSchema(ctx) }},
		{"backup", backups.EnsureSchema}, {"backup-navigation", backups.SeedNavigation},
		{"certification", func() error {
			return certification.New(db.G, nil, key, root+"/storage/certification").EnsureSchema(ctx)
		}},
		{"points", func() error { return points.NewService(db.G, nil).EnsureSchema(ctx) }},
		{"cdn", func() error { return cdnsell.NewService(db.G, key, nil).EnsureSchema(ctx) }},
		{"groupbuy", func() error { return groupbuy.NewService(db.G, nil, nil).EnsureSchema(ctx) }},
		{"marketplace", func() error { return marketplace.NewService(db.G, nil, nil).EnsureSchema(ctx) }},
		{"payment", func() error { return paymentrepo.EnsureSchemaAndDefaults(ctx, db.G) }},
		{"instance", func() error { return instance.EnsureSchema(ctx) }},
		{"instance-management", func() error { return instance.EnsureManagementSchema(ctx) }},
		{"video", func() error { return videobind.New(db.G, nil, key).EnsureSchema(ctx) }},
		{"video-navigation", func() error { return videobind.SeedNavigation(db.G) }},
		{"push", func() error { return pushtransfer.EnsureSchema(db.G) }},
		{"push-navigation", func() error { return pushtransfer.SeedNavigation(db.G) }},
		{"ticket", func() error { return ticketrepo.NewRepository(db.G).EnsureSchema(ctx) }},
	}
	for _, step := range steps {
		if err := step.run(); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}
