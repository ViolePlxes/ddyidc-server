package main

import (
	"context"
	"ddyidc/internal/adminread"
	"ddyidc/internal/backup"
	balance "ddyidc/internal/billing/balance"
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
	producthandler "ddyidc/internal/product/handler"
	productrepo "ddyidc/internal/product/repository"
	"ddyidc/internal/pushtransfer"
	"ddyidc/internal/remoteconsole"
	"ddyidc/internal/shared/distsync"
	"ddyidc/internal/shared/logger"
	"ddyidc/internal/shared/middleware"
	"ddyidc/internal/shared/runtimepaths"
	systemrepo "ddyidc/internal/system/repository"
	tickethandler "ddyidc/internal/ticket/handler"
	ticketrepo "ddyidc/internal/ticket/repository"
	ticketservice "ddyidc/internal/ticket/service"
	"ddyidc/internal/update"
	upstreamapi "ddyidc/internal/upstream/api"
	upstreamservice "ddyidc/internal/upstream/service"
	"ddyidc/internal/videobind"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	"ddyidc/pkg/clientip"
	cryptoutil "ddyidc/pkg/crypto"
	"ddyidc/pkg/db"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/zeromicro/go-zero/core/proc"
	"net/http"
	"time"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"
	"gorm.io/gorm"
)

var noCron = flag.Bool("no-cron", false, "关闭全部定时任务，仅提供 API")

var configFile = flag.String("f", "etc/config.yaml", "配置文件路径")
var migrate = flag.Bool("migrate", false, "执行 sql/01_create_tables.sql（开发环境，会重建表）")

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
	return h.marketing.AfterRegister(ctx, customerID, inviteCode, salesCode)
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
	_, _ = h.points.GrantByEvent(ctx, points.EventOrder, customerID, meta)
	_, _ = h.points.GrantByEvent(ctx, points.EventFirstPurchase, customerID, meta)
	return nil
}
func (h orderEventHooks) OnRechargePaid(ctx context.Context, rechargeID int64, rechargeNo string, customerID, amountCents int64) error {
	if err := h.marketing.OnRechargePaid(ctx, rechargeID, rechargeNo, customerID, amountCents); err != nil {
		return err
	}
	_, _ = h.points.GrantByEvent(ctx, points.EventRecharge, customerID, points.EventMeta{RefID: rechargeNo, AmountCents: amountCents, Remark: "充值赠送积分"})
	return nil
}

func (c paymentCoordinator) ProvisionAfterPayment(ctx context.Context, customerID, orderID int64, method string) error {
	if c.instances != nil {
		matched, err := c.instances.ProvisionRenewal(ctx, customerID, orderID, method)
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
func (c paymentCoordinator) AfterMarketplacePayment(ctx context.Context, orderID, customerID int64) error {
	return c.marketplace.AfterExternalPayment(ctx, orderID, customerID)
}

func main() {
	flag.Parse()

	var c struct {
		rest.RestConf
		Runtime       runtimepaths.Config `json:",optional"`
		Cron          *distsync.Config    `json:",optional"`
		Network       clientip.Config     `json:",optional"`
		MySQL         db.Config
		Redis         cache.Config
		Notify        notify.Config
		Update        update.Config
		RemoteConsole remoteconsole.Config
		Payment       struct{ AlipaySecretFile string }
		Crypto        struct{ AESKey string }
		JWT           struct {
			AccessSecret  string
			AccessExpire  int64
			RefreshSecret string
			RefreshExpire int64
		}
	}
	conf.MustLoad(*configFile, &c)
	clientIPs, err := clientip.New(c.Network)
	if err != nil {
		panic(err)
	}

	paths, err := runtimepaths.Resolve(c.Runtime)
	if err != nil {
		panic(err)
	}
	if err = paths.Prepare(); err != nil {
		panic(err)
	}
	if err = c.Update.UseDataDirectory(paths); err != nil {
		panic(err)
	}
	c.Log.Path = paths.Join("logs")
	logger.Init(c.Log)

	// 初始化数据库
	db.MustInit(c.MySQL)
	defer db.Close()
	if *migrate {
		if err := db.ApplySQLFile("../sql/01_create_tables.sql"); err != nil {
			panic("database migration failed: " + err.Error())
		}
	}
	if err := db.SeedDevelopmentData(); err != nil {
		panic("database seed failed: " + err.Error())
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
	server.Use(middleware.CookieSession(authManager))

	// 注册路由（各域 handler 在此注册）
	var cronStarts []func(context.Context)
	registerRoutes(server, notifyManager, updateManager, authManager, []byte(c.Crypto.AESKey), c.RemoteConsole, c.Payment.AlipaySecretFile, &cronStarts, paths)
	cronStarts = append(cronStarts, notifyManager.StartExpiryScanner, balance.New(db.G).StartReconciliation,
		func(ctx context.Context) { balance.New(db.G).StartNotifications(ctx, notifyManager) }, updateManager.StartResultWatcher)
	if !c.Cron.IsEnabled() || *noCron {
		fmt.Println("定时任务已关闭（测试模式），仅提供 API 服务")
	} else {
		cronCtx, cancelCron := context.WithCancel(context.Background())
		defer cancelCron()
		// go-zero's shutdown hook runs before resource teardown.
		proc.AddShutdownListener(cancelCron)
		go distsync.NewLeader(cache.RDB).Run(cronCtx, cronStarts)
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
	verificationService := notify.RegisterHandlers(server, notifyManager)
	notify.RegisterAdminHandlers(server, notifyManager, jwt)
	update.RegisterAdminHandlers(server, updateManager, jwt)
	upstreamapi.RegisterHandlers(server, upstreamservice.Default)
	adminread.RegisterHandlers(server, db.G, jwt)
	navigationSvc := navigation.New(db.G)
	if err := navigationSvc.EnsureSchema(context.Background()); err != nil {
		panic(err)
	}
	navigation.Register(server, navigationSvc, jwt)
	backupSvc := backup.New(db.G, snapshotKey, paths.Root)
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
	customerhandler.RegisterAuthHandlers(server, repositories.Customer, jwt, authEventHooks{marketing: marketingapi.NewAuthHooks(marketingSvc), points: pointsSvc}, verificationService)
	marketinghandler.RegisterUserHandlers(server, marketingSvc, jwt)
	marketinghandler.RegisterAdminHandlers(server, marketingSvc, jwt)
	marketinghandler.RegisterSalesHandlers(server, marketingSvc, jwt)

	// 订单/充值（M2/M4 挂钩分销与优惠券）
	orderSvc := orderservice.NewService(repositories.Order, orderEventHooks{marketing: marketingapi.NewOrderHooks(marketingSvc), points: pointsSvc}, snapshotKey)
	orderSvc.SetNotifier(notifyManager)
	cdnsellSvc := cdnsell.NewService(db.G, snapshotKey, notifyManager)
	if err := cdnsellSvc.EnsureSchema(context.Background()); err != nil {
		panic("cdnsell schema initialization failed: " + err.Error())
	}
	cdnsellEnabled, err := cdnsellSvc.Enabled(context.Background())
	if err != nil {
		panic("cdnsell configuration failed: " + err.Error())
	}
	var cdnsellPaymentSvc *cdnsell.Service
	if cdnsellEnabled {
		cdnsellPaymentSvc = cdnsellSvc
	}
	groupbuySvc := groupbuy.NewService(db.G, orderSvc, notifyManager)
	if err := groupbuySvc.EnsureSchema(context.Background()); err != nil {
		panic("groupbuy schema initialization failed: " + err.Error())
	}
	groupbuyEnabled, err := groupbuySvc.Enabled(context.Background())
	if err != nil {
		panic("groupbuy configuration failed: " + err.Error())
	}
	groupbuy.RegisterHandlers(server, groupbuySvc, jwt)
	if groupbuyEnabled {
		*cronStarts = append(*cronStarts, groupbuySvc.StartMaintenance)
	}
	marketplaceSvc := marketplace.NewService(db.G, cache.RDB, notifyManager)
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
	instanceSvc.SetManagement(db.G)
	if err := instanceSvc.EnsureManagementSchema(context.Background()); err != nil {
		panic("instance management schema failed: " + err.Error())
	}
	*cronStarts = append(*cronStarts, instanceSvc.StartRenewalMaintenance)
	paymentSvc := paymentservice.NewService(repositories.Payment, paymentConfig, paymentChannels, paymentservice.NewAsyncDispatcher(paymentCoordinator{instances: instanceSvc, certification: certificationSvc, orders: orderSvc, marketplace: marketplaceSvc, cdnsell: cdnsellPaymentSvc}))
	orderhandler.RegisterOrderHandlers(server, orderSvc, jwt, paymentSvc)
	paymenthandler.RegisterHandlers(server, paymentSvc, jwt)
	cdnsell.RegisterHandlers(server, cdnsellSvc, jwt)
	if cdnsellEnabled {
		*cronStarts = append(*cronStarts, cdnsellSvc.Start)
	}
	marketplace.RegisterHandlers(server, marketplaceSvc, jwt, paymentSvc)
	*cronStarts = append(*cronStarts, marketplaceSvc.StartMaintenance)
	marketplaceSvc.SetRefunder(paymentSvc)
	pointsEnabled, err := pointsSvc.Enabled(context.Background())
	if err != nil {
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
	if pointsEnabled {
		*cronStarts = append(*cronStarts, pointsSvc.StartExpiryScanner)
	}

	remoteRegistry := remoteconsole.NewRegistry()
	if err := remoteRegistry.Register(remoteconsole.NewNoVNCProvider(instanceSvc)); err != nil {
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
	ticketSvc := ticketservice.NewService(ticketrepo.NewRepository(db.G))
	ticketSvc.SetNotifier(notifyManager)
	if err := ticketSvc.EnsureSchema(context.Background()); err != nil {
		panic("ticket schema migration failed: " + err.Error())
	}
	tickethandler.RegisterHandlers(server, ticketSvc, jwt, paths.Join("storage/ticket-attachments"))

	producthandler.RegisterPublicHandlers(server, repositories.Product, upstreamservice.Default)
	// System route registration remains separate until its transport contract
	// is finalized.
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	var one int
	databaseOK := db.G != nil && db.G.WithContext(ctx).Raw("SELECT 1").Scan(&one).Error == nil && one == 1
	redisOK := cache.RDB != nil && cache.RDB.Ping(ctx).Err() == nil
	status := http.StatusOK
	if !databaseOK || !redisOK {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  map[bool]string{true: "ok", false: "degraded"}[databaseOK && redisOK],
		"version": version, "commit": commit, "branch": branch, "buildTime": buildTime,
		"database": databaseOK, "redis": redisOK,
	})
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{
		"version": version, "commit": commit, "branch": branch, "buildTime": buildTime,
	}})
}
