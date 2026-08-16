// Package web віддає статику дашборда, вшиту в бінар.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dashboard
var files embed.FS

// Dashboard — вміст internal/web/dashboard як корінь FS.
var Dashboard = dashboardFS()

func dashboardFS() fs.FS {
	sub, err := fs.Sub(files, "dashboard")
	if err != nil {
		panic(err)
	}
	return sub
}
