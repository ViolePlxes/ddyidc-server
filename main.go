package main

import (
	"context"
	"ddyidc/internal/adminread"
	"ddyidc/internal/admintodo"
	"ddyidc/internal/backup"
	balance "ddyidc/internal/billing/balance"
	"ddyidc/internal/billing/withdrawal"
	"ddyidc/internal/captcha"
	"ddyidc/internal/cdnsell"
	"ddyidc/internal/certification"
	customerhandler "ddyidc/internal/customer/handler"
	customerrrepo "ddyidc/internal/customer/repository"
	"ddyidc/internal/groupbuy"
	instancehandler "ddyidc/internal/instance/handler"
	instancerepo "ddyidc/internal/instance/repository"
	instanceservice "ddyidc/internal/instance/service"
	marketingapi "ddyidc/internal/marketing/api"
	marketinghandler "ddyidc/internal/marketing/handler"
	marketingrepo "ddyidc/internal/marketing/repository"
	marketingservice "ddyidc/internal/marketing/service"
	"ddyidc/internal/marketplace"
	"ddyidc/internal/navigation"
	"ddyidc/internal/notify"
	orderhandler "ddyidc/internal/order/handler"
	orderrepo "ddyidc/internal/order/repository"
	orderservice "ddyidc/internal/order/service"
	paymenthandler "ddyidc/internal/payment/handler"
	paymentrepo "ddyidc/internal/payment/repository"
	paymentservice "ddyidc/internal/payment/service"
	"ddyidc/internal/points"
	"ddyidc/internal/portal"
	producthandler "ddyidc/internal/product/handler"
	productrepo "ddyidc/internal/product/repository"
	"ddyidc/internal/promocoupon"
	"ddyidc/internal/pushtransfer"
	"ddyidc/internal/remoteconsole"
	"ddyidc/internal/security"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/logger"
	"ddyidc/internal/shared/middleware"
	"ddyidc/internal/shared/migrations"
	"ddyidc/internal/shared/runtimepaths"
	schemamode "ddyidc/internal/shared/schema"
	"ddyidc/internal/shared/snowflake"
	"ddyidc/internal/shared/tasks"
	"ddyidc/internal/shared/workgate"
	"ddyidc/internal/socialauth"
	systemrepo "ddyidc/internal/system/repository"
	tickethandler "ddyidc/internal/ticket/handler"
	ticketrepo "ddyidc/internal/ticket/repository"
	ticketservice "ddyidc/internal/ticket/service"
	"ddyidc/internal/update"
	upstreamapi "ddyidc/internal/upstream/api"
	upstreammetrics "ddyidc/internal/upstream/metrics"
	upstreamservice "ddyidc/internal/upstream/service"
	"ddyidc/internal/upstream/testpolicy"
	"ddyidc/internal/videobind"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/clientip"
	cryptoutil "ddyidc/pkg/crypto"
	"ddyidc/pkg/db"
	"ddyidc/pkg/outbound"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/zeromicro/go-zero/core/proc"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
)

var noCron = flag.Bool("no-cron", false, "关闭全部定时任务，仅提供 API")
var installBuildIsolation = flag.Bool("install-build-isolation", false, "root安装受限构建基础设施，不读取业务配置、不执行数据库迁移")
var cronOnly = flag.String("cron-only", "", "仅运行指定模块任务，支持 promo_coupon；与 -no-cron 互斥")
var promoMaintenance func(context.Context)
var draining atomic.Bool

var configFile = flag.String("f", "etc/config.yaml", "配置文件路径")
var migrate = flag.Bool("migrate", false, "执行离线版本迁移后退出；使用独立迁移账号配置")
var rollbackContractOnly = flag.Bool("rollback-contract", false, "仅输出当前二进制的数据兼容契约，不读取配置或访问数据库")
var verifyMigrationsOnly = flag.Bool("verify-migrations", false, "只读验证当前数据库迁移版本后退出，不启动业务服务")
var migrationSocket = flag.String("migration-socket", "", "仅root离线迁移使用本地MySQL socket；不改变运行账户权限")
var retryDirty = flag.Bool("retry-dirty-migration", false, "检查失败迁移的部分结果后显式重试")

var (
	version   = "dev"
	commit    = "unknown"
	branch    = "unknown"
	buildTime = "unknown"
)

var repositories struct {
	Customer  customerrrepo.Repository
	Product   productrepo.Repository
	Order     orderrepo.Repository
	Instance  instancerepo.Repository
	System    systemrepo.Repository
	Marketing marketingrepo.Repository
	Payment   paymentrepo.Repository
}

type paymentCoordinator struct {
	notifier      *notify.Manager
	groups        *groupbuy.Service
	instances     *instanceservice.Service
	certification *certification.Service
	orders        *orderservice.Service
	marketplace   *marketplace.Service
	cdnsell       *cdnsell.Service
}

type authEventHooks struct {
	marketing *marketingapi.AuthHooks
	points    *points.Service
}

func (h authEventHooks) VerifyInvite(ctx context.Context, code string) (string, error) {
	return h.marketing.VerifyInvite(ctx, code)
}
func (h authEventHooks) VerifySales(ctx context.Context, code string) (string, error) {
	return h.marketing.VerifySales(ctx, code)
}
func (h authEventHooks) AfterRegister(ctx context.Context, customerID int64, inviteCode, salesCode string) error {
	if err := h.marketing.AfterRegister(ctx, customerID, inviteCode, salesCode); err != nil {
		return err
	}
	return h.points.AfterInviteAttached(ctx, customerID)
}
func (h authEventHooks) AfterAuthEvent(ctx context.Context, event string, customerID int64) {
	_, _ = h.points.GrantByEvent(ctx, event, customerID, points.EventMeta{RefID: fmt.Sprintf("auth:%s:%d", event, time.Now().UnixNano())})
}

type orderEventHooks struct {
	marketing *marketingapi.OrderHooks
	points    *points.Service
}

func (h orderEventHooks) ChargeCoupon(ctx context.Context, tx *gorm.DB, userID, userCouponID, orderID, amountCents int64) (int64, error) {
	return h.marketing.ChargeCoupon(ctx, tx, userID, userCouponID, orderID, amountCents)
}
func (h orderEventHooks) ReleaseCoupon(ctx context.Context, tx *gorm.DB, orderID int64) error {
	return h.marketing.ReleaseCoupon(ctx, tx, orderID)
}
func (h orderEventHooks) OnOrderPaid(ctx context.Context, orderID int64, orderNo string, customerID, payCents int64) error {
	if err := h.marketing.OnOrderPaid(ctx, orderID, orderNo, customerID, payCents); err != nil {
		return err
	}
	meta := points.EventMeta{RefID: orderNo, AmountCents: payCents, Remark: "订单支付赠送积分"}
	if err := h.points.QueueEvent(ctx, points.EventOrder, customerID, meta); err != nil {
		return err
	}
	return h.points.QueueEvent(ctx, points.EventFirstPurchase, customerID, meta)
}
func (h orderEventHooks) OnRechargePaid(ctx context.Context, rechargeID int64, rechargeNo string, customerID, amountCents int64) error {
	if err := h.marketing.OnRechargePaid(ctx, rechargeID, rechargeNo, customerID, amountCents); err != nil {
		return err
	}
	return h.points.QueueEvent(ctx, points.EventRecharge, customerID, points.EventMeta{RefID: rechargeNo, AmountCents: amountCents, Remark: "充值赠送积分"})
}

func (c paymentCoordinator) ProvisionAfterPayment(ctx context.Context, customerID, orderID int64, method string) error {
	if c.instances != nil {
		matched, err := c.instances.ProvisionUpgrade(ctx, customerID, orderID, method)
		if matched || err != nil {
			return err
		}
		matched, err = c.instances.ProvisionRenewal(ctx, customerID, orderID, method)
		if matched || err != nil {
			return err
		}
	}
	if c.certification != nil {
		matched, err := c.certification.ProvisionPlatformOrder(ctx, customerID, orderID, method)
		if matched || err != nil {
			return err
		}
	}
	if c.cdnsell != nil {
		matched, err := c.cdnsell.ProvisionPlatformOrder(ctx, customerID, orderID, method)
		if matched || err != nil {
			return err
		}
	}
	return c.orders.ProvisionAfterPayment(ctx, customerID, orderID, method)
}
func (c paymentCoordinator) AfterRechargePayment(ctx context.Context, rechargeID, customerID, amountCents int64) error {
	return c.orders.AfterRechargePayment(ctx, rechargeID, customerID, amountCents)
}
func (c paymentCoordinator) ProvisionDeliveryUnit(ctx context.Context, customerID, unitID int64, method string) error {
	return c.orders.ProvisionDeliveryUnit(ctx, customerID, unitID, method)
}
func (c paymentCoordinator) AfterOrderDelivery(ctx context.Context, customerID, orderID int64) error {
	if err := c.groups.SyncDeliveredOrder(ctx, customerID, orderID); err != nil {
		return err
	}
	return c.orders.AfterOrderDelivery(ctx, customerID, orderID)
}
func (c paymentCoordinator) NotifyDeliveredInstance(ctx context.Context, customerID, instanceID int64) error {
	return c.orders.NotifyDeliveredInstance(ctx, customerID, instanceID)
}
func (c paymentCoordinator) RefreshWebhook(ctx context.Context, p tasks.RefreshPayload) error {
	return c.instances.RefreshWebhook(ctx, p)
}
func (c paymentCoordinator) DeliverNotice(ctx context.Context, p tasks.NoticePayload) error {
	if c.notifier == nil {
		return errors.New("notification service unavailable")
	}
	return c.notifier.NotifyCustomer(ctx, p.Scene, p.CustomerID, fmt.Sprint(p.BusinessID), p.Params)
}
func (c paymentCoordinator) NotifyGroupStatus(ctx context.Context, customerID, recordID int64, scene string) error {
	return c.groups.NotifyStatus(ctx, customerID, recordID, scene)
}
func (c paymentCoordinator) NotifyGroupDelivered(ctx context.Context, customerID, recordID int64) error {
	return c.groups.NotifyDelivered(ctx, customerID, recordID)
}
func (c paymentCoordinator) CompleteGroup(ctx context.Context, id int64) error {
	return c.groups.CompleteGroup(ctx, id)
}
func (c paymentCoordinator) AfterMarketplacePayment(ctx context.Context, orderID, customerID int64) error {
	return c.marketplace.AfterExternalPayment(ctx, orderID, customerID)
}

func main() {
	flag.Parse()
	if *rollbackContractOnly {
		fmt.Println(update.BackendRollbackContract)
		return
	}
	if *installBuildIsolation {
		if err := installReleaseInfrastructure(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	var c struct {
		rest.RestConf
		Runtime               runtimepaths.Config `json:",optional"`
		DeploymentEnvironment string              `json:",optional"`
		UpstreamTestPolicy    testpolicy.Config   `json:",optional"`
		Outbound              outbound.Config     `json:",optional"`
		Cron                  *distsync.Config    `json:",optional"`
		Network               clientip.Config     `json:",optional"`
		MySQL                 db.Config
		Redis                 cache.Config
		Notify                notify.Config
		Update                update.Config
		RemoteConsole         remoteconsole.Config
		Payment               struct{ AlipaySecretFile string }
		Crypto                struct{ AESKey string }
		JWT                   struct {
			AccessSecret  string
			AccessExpire  int64
			RefreshSecret string
			RefreshExpire int64
		}
	}
	conf.MustLoad(*configFile, &c)
	if c.Outbound.DetailReadOnly && !*noCron {
		panic("detail-only migration mode requires -no-cron")
	}
	if err := outbound.Configure(c.Outbound); err != nil {
		panic("invalid outbound network policy")
	}
	if err := testpolicy.Configure(c.DeploymentEnvironment, c.UpstreamTestPolicy); err != nil {
		panic(err)
	}
	clientIPs, err := clientip.New(c.Network)
	if err != nil {
		panic(err)
	}

	paths, err := runtimepaths.Resolve(c.Runtime)
	if err != nil {
		panic(err)
	}
	if !*migrate && !*verifyMigrationsOnly {
		if err = paths.Prepare(); err != nil {
			panic(err)
		}
		if err = c.Update.UseDataDirectory(paths); err != nil {
			panic(err)
		}
		c.Log.Path = paths.Join("logs")
		logger.Init(c.Log)

	}
	// 初始化数据库
	c.MySQL.Migration = *migrate
	if *migrationSocket != "" {
		if err := validateMigrationSocket(*migrate, os.Geteuid(), *migrationSocket, c.MySQL.Host); err != nil {
			panic(err)
		}
		if err := db.InitOfflineMigrationSocket(*migrationSocket, c.MySQL.Database, c.MySQL.Port); err != nil {
			panic("offline migration socket unavailable")
		}
		var port int
		if err := db.DB.QueryRow("SELECT @@port").Scan(&port); err != nil || port != c.MySQL.Port {
			panic("migration socket does not match configured local database")
		}
	} else {
		db.MustInit(c.MySQL)
	}
	defer db.Close()
	migrationSteps, err := migrations.Steps()
	if err != nil {
		panic(err)
	}
	if *verifyMigrationsOnly {
		if *migrate || *retryDirty || *migrationSocket != "" {
			panic("verify-migrations cannot perform migration")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := migrations.Verify(ctx, db.DB, migrationSteps); err != nil {
			fmt.Fprintln(os.Stderr, "Database migration preflight failed:", err)
			os.Exit(2)
		}
		if err := upstreamservice.NewDefaultStore(nil).ValidateRetainedModules(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "Adapter compatibility preflight failed:", err)
			os.Exit(2)
		}
		// The candidate is invoked by old and new online update managers alike.
		// Refuse an unsafe backup before switching, without changing business data.
		if current := c.Update.Backend.Executable; current != "" {
			self, err := os.Executable()
			if err != nil {
				panic("candidate executable unavailable")
			}
			if current != self {
				if err := update.VerifyBackendRollbackContract(ctx, update.OSRunner{}, current); err != nil {
					if bootstrapErr := update.VerifyPristineCDNBootstrap(ctx, db.DB); bootstrapErr != nil {
						fmt.Fprintln(os.Stderr, "Rollback compatibility preflight failed:", bootstrapErr)
						os.Exit(2)
					}
					fmt.Println("Pristine disabled CDN bootstrap verified; concurrent inserts and enabling are guarded")
				}
			}
		}
		fmt.Println("Read-only database migration preflight complete; no service or scheduled task started")
		return
	}
	if *migrate {
		schemamode.EnableMigrations()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := migrations.Apply(ctx, db.DB, migrationSteps, *retryDirty, func(ctx context.Context) error { return seedModules(ctx, []byte(c.Crypto.AESKey), paths.Root) }); err != nil {
			panic(err)
		}
		if *migrationSocket != "" {
			if err := notify.ApplyDigestRuntimePrivileges(ctx, db.DB, c.MySQL.User, c.MySQL.Host); err != nil {
				panic(err)
			}
			if err := points.ApplyRuntimePrivileges(ctx, db.DB, c.MySQL.User, c.MySQL.Host); err != nil {
				panic(err)
			}
			if err := promocoupon.ApplyRuntimePrivileges(ctx, db.DB, c.MySQL.User, c.MySQL.Host); err != nil {
				panic(err)
			}
			if err := instanceservice.ApplyRuntimePrivileges(ctx, db.DB, c.MySQL.User, c.MySQL.Host); err != nil {
				panic(err)
			}
			if err := security.ApplyRuntimePrivileges(ctx, db.DB, c.MySQL.User, c.MySQL.Host); err != nil {
				panic(err)
			}
		}
		fmt.Println("Offline migrations complete; API and scheduled jobs were not started")
		return
	}
	if *retryDirty {
		panic("retry-dirty-migration requires migrate")
	}
	if err := migrations.Verify(context.Background(), db.DB, migrationSteps); err != nil {
		panic(err)
	}
	controlPool, err := db.OpenControl(c.MySQL)
	if err != nil {
		panic("maintenance control connection unavailable")
	}
	defer controlPool.Close()
	workgate.Configure(controlPool)
	if err := workgate.Run(context.Background(), func(context.Context) error { return nil }); err != nil {
		panic("maintenance barrier is closed")
	}
	if err := snowflake.Configure(context.Background(), db.DB); err != nil {
		panic(err)
	}
	if err := cryptoutil.ValidateKey([]byte(c.Crypto.AESKey)); err != nil {
		panic("invalid Crypto.AESKey: AES-GCM requires a 16, 24, or 32 byte key")
	}
	upstreamservice.Default = upstreamservice.NewDefaultStore([]byte(c.Crypto.AESKey))
	if err := upstreamservice.Default.EnsureSyncSchema(); err != nil {
		panic("upstream sync schema failed: " + err.Error())
	}
	initRepositories()
	if err := repositories.Product.EnsureSpecsSchema(context.Background()); err != nil {
		panic("product specs schema migration failed: " + err.Error())
	}

	// 营销配置默认值（M6，幂等）
	if err := marketingservice.NewService(db.G, repositories.Marketing).EnsureDefaults(context.Background()); err != nil {
		panic("marketing defaults seed failed: " + err.Error())
	}

	// 初始化缓存
	cache.MustInit(c.Redis)
	authManager := auth.NewManager(c.JWT.AccessSecret, c.JWT.RefreshSecret, c.JWT.AccessExpire, c.JWT.RefreshExpire)
	if err := authManager.LoadSecurityConfig(context.Background(), db.G); err != nil {
		panic("security config initialization failed: " + err.Error())
	}
	notifyManager, err := notify.NewManager(db.G, c.Notify, []byte(c.Crypto.AESKey))
	if err != nil {
		panic("notify initialization failed: " + err.Error())
	}
	updateManager, err := update.NewManager(db.G, cache.RDB, c.Update)
	if err != nil {
		panic("update initialization failed: " + err.Error())
	}

	// 创建服务
	c.RestConf.Middlewares.Log = false // Certification requests must never enter the body-copying logger.
	server := rest.MustNewServer(c.RestConf)
	server.Use(clientIPs.Middleware)
	server.Use(certification.RequestLogger)
	defer server.Stop()

	// 全局中间件
	server.Use(middleware.CORS())
	server.Use(middleware.RequestID())
	server.Use(middleware.Logger())
	server.Use(middleware.RedactSensitiveHeaders())
	accessSecurity := security.New(db.G, []byte(c.Crypto.AESKey))
	authManager.SetSessionValidator(accessSecurity.ValidateSession)
	server.Use(accessSecurity.Middleware)
	server.Use(middleware.CookieSession(authManager))
	server.Use(workgate.Middleware)

	// 注册路由（各域 handler 在此注册）
	if *cronOnly != "" && (*cronOnly != "promo_coupon" || *noCron) {
		panic("invalid cron-only selection")
	}
	var cronStarts []func(context.Context)
	proc.AddShutdownListener(func() { draining.Store(true) })
	registerRoutes(server, notifyManager, updateManager, authManager, []byte(c.Crypto.AESKey), c.RemoteConsole, c.Payment.AlipaySecretFile, &cronStarts, paths)
	cronStarts = append(cronStarts, notifyManager.StartExpiryScanner, notifyManager.StartDigestWorker, balance.New(db.G).StartReconciliation,
		func(ctx context.Context) { balance.New(db.G).StartNotifications(ctx, notifyManager) },
		func(ctx context.Context) {
			withdrawal.WithdrawalService{DB: db.G}.StartNotifications(ctx, notifyManager)
		}, updateManager.StartResultWatcher)
	if *cronOnly == "promo_coupon" {
		cronStarts = []func(context.Context){promoMaintenance}
	}
	if (!c.Cron.IsEnabled() && *cronOnly == "") || *noCron {
		fmt.Println("定时任务已关闭（测试模式），仅提供 API 服务")
	} else {
		cronCtx, cancelCron := context.WithCancel(upstreammetrics.WithBackground(context.Background()))
		defer cancelCron()
		// go-zero's shutdown hook runs before resource teardown.
		proc.AddShutdownListener(cancelCron)
		go distsync.NewLeader(cache.RDB).WithAdmission(workgate.Run).Run(cronCtx, cronStarts)
	}

	fmt.Printf("DDYIDC 服务启动，监听 %s:%d\n", c.Host, c.Port)
	server.Start()
}

func initRepositories() {
	repositories.Customer = customerrrepo.NewRepository(db.G)
	repositories.Product = productrepo.NewRepository(db.G)
	repositories.Order = orderrepo.NewRepository(db.G)
	repositories.Instance = instancerepo.NewRepository(db.G)
	repositories.System = systemrepo.NewRepository(db.G)
	repositories.Marketing = marketingrepo.NewRepository(db.G)
	repositories.Payment = paymentrepo.NewRepository(db.G)
}

func registerRoutes(server *rest.Server, notifyManager *notify.Manager, updateManager *update.Manager, jwt *auth.Manager, snapshotKey []byte, remoteConfig remoteconsole.Config, alipaySecretFile string, cronStarts *[]func(context.Context), paths runtimepaths.Paths) {
	// 健康检查
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/health",
		Handler: healthHandler,
	})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/system/version", Handler: versionHandler})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/health/live", Handler: liveHandler})
	server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/health/ready", Handler: healthHandler})
	verificationService := notify.RegisterHandlers(server, notifyManager)
	notify.RegisterAdminHandlers(server, notifyManager, jwt, balance.New(db.G))
	update.RegisterAdminHandlers(server, updateManager, jwt)
	upstreamapi.RegisterHandlers(server, upstreamservice.Default)
	upstreamapi.RegisterModuleHandlers(server, upstreamservice.Default, jwt)
	upstreamapi.RegisterSupplierSnapshotHandlers(server, upstreamservice.Default, jwt)
	adminread.RegisterHandlers(server, db.G, jwt, paths.Root)
	admintodo.Register(server, db.G, jwt, snapshotKey)
	admintodo.RegisterExpiryWorkflows(server, db.G, jwt, notifyManager)
	navigationSvc := navigation.New(db.G)
	if err := navigationSvc.EnsureSchema(context.Background()); err != nil {
		panic(err)
	}
	navigation.Register(server, navigationSvc, jwt)
	portalSvc := portal.New(db.G, paths.Root)
	if err := portalSvc.Prepare(); err != nil {
		panic("portal runtime paths unavailable")
	}
	portal.Register(server, portalSvc, jwt)
	*cronStarts = append(*cronStarts, portalSvc.Start)
	backupSvc := backup.New(db.G, snapshotKey, paths.Root)
	if err := backupSvc.PreparePaths(); err != nil {
		panic("backup runtime paths unavailable")
	}
	if err := backupSvc.EnsureSchema(); err != nil {
		panic("backup schema initialization failed")
	}
	if err := backupSvc.SeedNavigation(); err != nil {
		panic("backup navigation initialization failed")
	}
	backup.Register(server, backupSvc, jwt)

	certificationSvc := certification.New(db.G, cache.RDB, snapshotKey, paths.Join("storage/certification"))
	if err := certificationSvc.EnsureSchema(context.Background()); err != nil {
		panic("certification initialization failed: " + err.Error())
	}
	certification.Register(server, certificationSvc, jwt)

	// 营销套件（M1-M6）
	marketingSvc := marketingservice.NewService(db.G, repositories.Marketing)
	pointsSvc := points.NewService(db.G, cache.RDB)
	if err := pointsSvc.EnsureSchema(context.Background()); err != nil {
		panic("points schema initialization failed: " + err.Error())
	}
	accessSecurity := security.New(db.G, snapshotKey)
	accessSecurity.Register(server, jwt)
	humanVerification := captcha.New(db.G, cache.RDB, snapshotKey)
	humanVerification.Register(server, jwt, accessSecurity)
	customerhandler.RegisterAuthHandlers(server, repositories.Customer, jwt, authEventHooks{marketing: marketingapi.NewAuthHooks(marketingSvc), points: pointsSvc}, verificationService, accessSecurity, humanVerification)
	socialauth.Register(server, db.G, jwt, snapshotKey, verificationService)
	marketinghandler.RegisterUserHandlers(server, marketingSvc, jwt)
	marketinghandler.RegisterAdminHandlers(server, marketingSvc, jwt)
	marketinghandler.RegisterSalesHandlers(server, marketingSvc, jwt)

	// 订单/充值（M2/M4 挂钩分销与优惠券）
	orderSvc := orderservice.NewService(repositories.Order, orderEventHooks{marketing: marketingapi.NewOrderHooks(marketingSvc), points: pointsSvc}, snapshotKey)
	pointsSvc.SetOrderService(orderSvc)
	promoCouponSvc := promocoupon.New(db.G, pointsSvc, snapshotKey)
	marketingSvc.SetFundedCouponHooks(promoCouponSvc.GuardCouponTx, promoCouponSvc.CouponChargedTx)
	marketingSvc.SetFundedCouponRelease(promoCouponSvc.CouponReleasedTx)
	promoCouponSvc.SetCouponApply(marketingSvc.ChargeCouponForOrder)
	orderSvc.RegisterModuleBalancePayer("promo_coupon", promoCouponSvc.PayBalance)
	promocoupon.Register(server, promoCouponSvc, jwt)
	promoMaintenance = promoCouponSvc.StartMaintenance
	*cronStarts = append(*cronStarts, promoCouponSvc.StartMaintenance)
	*cronStarts = append(*cronStarts, orderSvc.StartStockExpiry)
	orderSvc.SetNotifier(notifyManager)
	cdnsellSvc := cdnsell.NewService(db.G, snapshotKey, notifyManager)
	if err := cdnsellSvc.EnsureSchema(context.Background()); err != nil {
		panic("cdnsell schema initialization failed: " + err.Error())
	}
	_, err := cdnsellSvc.Enabled(context.Background())
	if err != nil {
		panic("cdnsell configuration failed: " + err.Error())
	}
	cdnsellPaymentSvc := cdnsellSvc
	groupbuySvc := groupbuy.NewService(db.G, orderSvc, notifyManager)
	if err := groupbuySvc.EnsureSchema(context.Background()); err != nil {
		panic("groupbuy schema initialization failed: " + err.Error())
	}
	_, err = groupbuySvc.Enabled(context.Background())
	if err != nil {
		panic("groupbuy configuration failed: " + err.Error())
	}
	groupbuy.RegisterHandlers(server, groupbuySvc, jwt)
	*cronStarts = append(*cronStarts, groupbuySvc.StartMaintenance)
	marketplaceSvc := marketplace.NewService(db.G, cache.RDB, notifyManager)
	marketplaceSvc.SetWithdrawalKey(snapshotKey)
	if err := marketplaceSvc.EnsureSchema(context.Background()); err != nil {
		panic("marketplace schema initialization failed: " + err.Error())
	}
	_, err = marketplaceSvc.Enabled(context.Background())
	if err != nil {
		panic("marketplace configuration failed: " + err.Error())
	}
	if err := paymentrepo.EnsureSchemaAndDefaults(context.Background(), db.G); err != nil {
		panic("payment schema initialization failed: " + err.Error())
	}
	paymentConfig, err := paymentservice.LoadConfigWithKey(context.Background(), db.G, snapshotKey, alipaySecretFile)
	if err != nil {
		panic("payment config initialization failed: " + err.Error())
	}
	paymentChannels, err := paymentservice.BuildChannels(context.Background(), paymentConfig)
	if err != nil {
		panic("payment channel initialization failed: " + err.Error())
	}
	instanceSvc := instanceservice.NewService(repositories.Instance, upstreamservice.Default)
	upstreamservice.Default.SetSupplierSnapshotWorker(instanceSvc)
	*cronStarts = append(*cronStarts, upstreamservice.Default.StartSupplierSnapshotScheduler)
	instanceSvc.SetManagement(db.G)
	orderSvc.SetRenewalBalancePayer(instanceSvc)
	if err := instanceSvc.EnsureManagementSchema(context.Background()); err != nil {
		panic("instance management schema failed: " + err.Error())
	}
	*cronStarts = append(*cronStarts, instanceSvc.StartRenewalMaintenance, instanceSvc.StartMachineRefundMaintenance, instanceSvc.StartUpgradeMaintenance)
	durableDispatcher := paymentservice.NewDeferredDurableDispatcher(db.G, paymentCoordinator{notifier: notifyManager, groups: groupbuySvc, instances: instanceSvc, certification: certificationSvc, orders: orderSvc, marketplace: marketplaceSvc, cdnsell: cdnsellPaymentSvc})
	*cronStarts = append(*cronStarts, durableDispatcher.Start)
	paymentSvc := paymentservice.NewService(repositories.Payment, paymentConfig, paymentChannels, durableDispatcher)
	paymentSvc.SetRemoteDeliveryRecovery(orderSvc.ReconcileRemoteDelivery)
	paymentSvc.SetRenewalRecovery(instanceSvc.ReconcileRenewal)
	admintodo.RegisterProductWorkflows(server, db.G, jwt, paymentSvc)
	orderhandler.RegisterOrderHandlers(server, orderSvc, jwt, paymentSvc)
	paymenthandler.RegisterHandlers(server, paymentSvc, jwt)
	paymenthandler.RegisterSettings(server, db.G, jwt, paymentservice.NewSettingsManager(db.G, snapshotKey, paymentConfig, alipaySecretFile))
	cdnsell.RegisterHandlers(server, cdnsellSvc, jwt)
	*cronStarts = append(*cronStarts, cdnsellSvc.Start)
	marketplace.RegisterHandlers(server, marketplaceSvc, jwt, paymentSvc)
	*cronStarts = append(*cronStarts, marketplaceSvc.StartMaintenance)
	marketplaceSvc.SetRefunder(paymentSvc)
	if _, err := pointsSvc.Enabled(context.Background()); err != nil {
		panic("points configuration failed: " + err.Error())
	}
	points.RegisterHandlers(server, pointsSvc, jwt)
	videoSvc := videobind.New(db.G, cache.RDB, snapshotKey)
	if err := videoSvc.EnsureSchema(context.Background()); err != nil {
		panic("video schema initialization failed")
	}
	videobind.RegisterHandlers(server, videoSvc, jwt)
	if err := videobind.SeedNavigation(db.G); err != nil {
		panic("video navigation initialization failed")
	}
	*cronStarts = append(*cronStarts, pointsSvc.StartMaintenance)

	remoteRegistry := remoteconsole.NewRegistry()
	remoteProvider := remoteconsole.NewNoVNCProvider(instanceSvc)
	if err := remoteProvider.SetNodeID(remoteConfig.NodeID); err != nil {
		panic("invalid remote console node identity")
	}
	if err := remoteRegistry.Register(remoteProvider); err != nil {
		panic("remote console provider initialization failed")
	}
	if err := remoteRegistry.Use(remoteConfig.Engine); err != nil {
		panic("remote console engine is not registered")
	}
	instanceSvc.SetRemoteConsole(remoteRegistry)
	if err := instanceSvc.EnsureSchema(context.Background()); err != nil {
		panic("instance schema migration failed: " + err.Error())
	}
	if err := pushtransfer.EnsureSchema(db.G); err != nil {
		panic("push transfer schema initialization failed: " + err.Error())
	}
	if err := pushtransfer.SeedNavigation(db.G); err != nil {
		panic("push navigation migration failed: " + err.Error())
	}
	pushtransfer.RegisterHandlers(server, pushtransfer.New(db.G, cache.RDB), jwt)
	instancehandler.RegisterHandlers(server, instanceSvc, jwt)
	admintodo.RegisterManualEventReviews(server, db.G, jwt, instanceSvc)
	admintodo.RegisterManualProductActions(server, db.G, jwt, instanceSvc)
	admintodo.RegisterMachineRefundActions(server, db.G, jwt, instanceSvc)
	ticketSvc := ticketservice.NewService(ticketrepo.NewRepository(db.G))
	ticketSvc.SetNotifier(notifyManager)
	if err := ticketSvc.EnsureSchema(context.Background()); err != nil {
		panic("ticket schema migration failed: " + err.Error())
	}
	tickethandler.RegisterHandlers(server, ticketSvc, jwt, paths.Join("storage/ticket-attachments"))
	*cronStarts = append(*cronStarts, func(ctx context.Context) {
		ticketSvc.StartUploadMaintenance(ctx, paths.Join("storage/ticket-attachments"))
	})

	producthandler.RegisterPublicHandlers(server, repositories.Product, upstreamservice.Default)
	// System route registration remains separate until its transport contract
	// is finalized.
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if draining.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "draining"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var one int
	databaseOK := db.G != nil && db.G.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error == nil && one == 1
	redisOK := cache.RDB != nil && cache.RDB.Ping(ctx).Err() == nil
	var barrierMode string
	maintenanceOK := db.G != nil && db.G.WithContext(ctx).Raw("SELECT mode FROM system.operation_barrier WHERE id=1").Scan(&barrierMode).Error == nil && barrierMode == "running"
	status := http.StatusOK
	if !databaseOK || !redisOK || !maintenanceOK {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  map[bool]string{true: "ok", false: "degraded"}[databaseOK && redisOK && maintenanceOK],
		"version": version, "commit": commit, "branch": branch, "buildTime": buildTime,
		"database": databaseOK, "redis": redisOK, "maintenanceReady": maintenanceOK,
	})
}

func liveHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "alive"})
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{
		"version": version, "commit": commit, "branch": branch, "buildTime": buildTime,
	}})
}
