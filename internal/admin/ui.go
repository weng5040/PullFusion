package admin

import _ "embed"

//go:embed dashboard.html
var dashboardRaw string

//go:embed dbconsole.html
var dbconsoleRaw string

func init() {
	DashboardHTML = []byte(dashboardRaw)
	DBConsoleHTML = []byte(dbconsoleRaw)
}

var DashboardHTML []byte
var DBConsoleHTML []byte
