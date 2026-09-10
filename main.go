package main

import (
	"context"
	customerhandler "ddyidc/internal/customer/handler"
	customerrrepo "ddyidc/internal/customer/repository"
	instancehandler "ddyidc/internal/instance/handler"
	instancerepo "ddyidc/internal/instance/repository"
	instanceservice "ddyidc/internal/instance/service"
	marketingapi "ddyidc/internal/marketing/api"
	marketinghandler "ddyidc/internal/marketing/handler"
	marketingrepo "ddyidc/internal/marketing/repository"
	marketingservice "ddyidc/internal/marketing/service"
	"ddyidc/internal/notify"
	orderhandler "ddyidc/internal/order/handler"
	orderrepo "ddyidc/internal/order/repository"
	orderservice "ddyidc/internal/order/service"
	paymenthandler "ddyidc/internal/payment/handler"
	paymentrepo "ddyidc/internal/payment/repository"
	paymentservice "ddyidc/internal/payment/service"
	producthandler "ddyidc/internal/product/handler"
	productrepo "ddyidc/internal/product/repository"
	"ddyidc/internal/shared/logger"
	"ddyidc/internal/shared/middleware"
	systemrepo "ddyidc/internal/system/repository"
	tickethandler "ddyidc/internal/ticket/handler"
	ticketrepo "ddyidc/internal/ticket/repository"
	ticketservice "ddyidc/internal/ticket/service"
	upstreamapi "ddyidc/internal/upstream/api"
	upstreamservice "ddyidc/internal/upstream/service"
	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
	cryptoutil "ddyidc/pkg/crypto"
	"ddyidc/pkg/db"
	"flag"
	"fmt"
	"net/http"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"
)

var configFile = flag.String("f", "etc/config.yaml", "配置文件路径")
var migrate = flag.Bool("migrate", false, "执行 sql/01_create_tables.sql（开发环境，会重建表）")

var repositories struct {
	Customer  customerrrepo.Repository
	Product   productrepo.Repository
	Order     orderrepo.Repository
	Instance  instancerepo.Repository
	System    systemrepo.Repository
	Marketing marketingrepo.Repository
	Payment   paymentrepo.Repository
}

func main() {
	flag.Parse()

	var c struct {
		rest.RestConf
		MySQL  db.Config
		Redis  cache.Config
		Notify notify.Config
		Crypto struct{ AESKey string }
		JWT    struct {
			AccessSecret  string
			AccessExpire  int64
			RefreshSecret string
			RefreshExpire int64
		}
	}
	conf.MustLoad(*configFile, &c)

	// 初始化日志
	logger.Init(c.Mode)

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

	// 创建服务
	server := rest.MustNewServer(c.RestConf)
	defer server.Stop()

	// 全局中间件
	server.Use(middleware.CORS())
	server.Use(middleware.CookieSession(authManager))
	server.Use(middleware.RequestID())
	server.Use(middleware.Logger())

	// 注册路由（各域 handler 在此注册）
	registerRoutes(server, notifyManager, authManager, []byte(c.Crypto.AESKey))
	notifyManager.StartExpiryScanner(context.Background())

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

func registerRoutes(server *rest.Server, notifyManager *notify.Manager, jwt *auth.Manager, snapshotKey []byte) {
	// 健康检查
	server.AddRoute(rest.Route{
		Method:  http.MethodGet,
		Path:    "/health",
		Handler: healthHandler,
	})
	notify.RegisterHandlers(server, notifyManager)
	notify.RegisterAdminHandlers(server, notifyManager, jwt)
	upstreamapi.RegisterHandlers(server, upstreamservice.Default)

	// 营销套件（M1-M6）
	marketingSvc := marketingservice.NewService(db.G, repositories.Marketing)
	customerhandler.RegisterAuthHandlers(server, repositories.Customer, jwt, marketingapi.NewAuthHooks(marketingSvc))
	marketinghandler.RegisterUserHandlers(server, marketingSvc, jwt)
	marketinghandler.RegisterAdminHandlers(server, marketingSvc, jwt)
	marketinghandler.RegisterSalesHandlers(server, marketingSvc, jwt)

	// 订单/充值（M2/M4 挂钩分销与优惠券）
	orderSvc := orderservice.NewService(repositories.Order, marketingapi.NewOrderHooks(marketingSvc), snapshotKey)
	orderSvc.SetNotifier(notifyManager)
	orderhandler.RegisterOrderHandlers(server, orderSvc, jwt)
	if err := paymentrepo.EnsureSchemaAndDefaults(context.Background(), db.G); err != nil {
		panic("payment schema initialization failed: " + err.Error())
	}
	paymentConfig, err := paymentservice.LoadConfig(context.Background(), db.G)
	if err != nil {
		panic("payment config initialization failed: " + err.Error())
	}
	paymentChannels, err := paymentservice.BuildChannels(context.Background(), paymentConfig)
	if err != nil {
		panic("payment channel initialization failed: " + err.Error())
	}
	paymentSvc := paymentservice.NewService(repositories.Payment, paymentConfig, paymentChannels, paymentservice.NewAsyncDispatcher(orderSvc))
	paymenthandler.RegisterHandlers(server, paymentSvc, jwt)
	instanceSvc := instanceservice.NewService(repositories.Instance, upstreamservice.Default)
	if err := instanceSvc.EnsureSchema(context.Background()); err != nil {
		panic("instance schema migration failed: " + err.Error())
	}
	instancehandler.RegisterHandlers(server, instanceSvc, jwt)
	ticketSvc := ticketservice.NewService(ticketrepo.NewRepository(db.G))
	ticketSvc.SetNotifier(notifyManager)
	if err := ticketSvc.EnsureSchema(context.Background()); err != nil {
		panic("ticket schema migration failed: " + err.Error())
	}
	tickethandler.RegisterHandlers(server, ticketSvc, jwt)

	producthandler.RegisterPublicHandlers(server, repositories.Product, upstreamservice.Default)
	// System route registration remains separate until its transport contract
	// is finalized.
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}
