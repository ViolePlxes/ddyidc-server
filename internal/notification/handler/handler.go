package notification

import "net/http"

// RegisterHandlers 注册 notification 域的 HTTP 路由
// 在 main.go 的 registerRoutes 中调用
func RegisterHandlers(server interface{}) {
    // TODO: 注册路由
    // server.AddRoute(rest.Route{Method: http.MethodGet, Path: "/api/v1/notifications", Handler: ListHandler})
}

// TODO: 实现各 HTTP handler（参数校验 -> 调用 service -> 返回响应）
func ExampleHandler(w http.ResponseWriter, r *http.Request) {
    // 示例 handler
}
