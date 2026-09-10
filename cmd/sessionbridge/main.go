package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"ddyidc/pkg/auth"
	"ddyidc/pkg/cache"
)

const (
	customerID = int64(7327444989321224)
	account    = "e2eqk09091047"
)

func main() {
	cache.MustInit(cache.Config{Host: "127.0.0.1", Port: 6379, PoolSize: 2})
	manager := auth.NewManager(
		"ddyidc-access-secret-change-in-production",
		"ddyidc-refresh-secret-change-in-production",
		7200,
		604800,
	)
	token, err := manager.IssueSession(context.Background(), auth.SessionUser, customerID, account, "customer")
	if err != nil {
		panic(err)
	}

	server := &http.Server{Addr: "127.0.0.1:3199", ReadHeaderTimeout: 5 * time.Second}
	http.HandleFunc("/enter", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "ddyidc_token",
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   900,
		})
		http.Redirect(w, r, "http://localhost:3100/user/instance/7327450863063045.htm", http.StatusFound)
		go func() {
			time.Sleep(time.Second)
			_ = server.Shutdown(context.Background())
		}()
	})
	fmt.Println("session bridge listening on http://localhost:3199/enter")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
