// Package web holds the web app's build inside the Go binary.
package web

import (
	"embed"
	"io/fs"
)

// dist is the folder `npm run build` writes here. Git ignores the build and
// keeps dist/.gitkeep, so this compiles in a checkout that has not built
// the app. A build empties dist and copies public/ into it, so
// public/.gitkeep puts the kept file back.
//
//go:embed all:dist
var dist embed.FS

// App is the build this binary was compiled with: index.html and assets/.
// It is nil when the app was not built before the binary was.
func App() fs.FS {
	app, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	if info, err := fs.Stat(app, "index.html"); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	return app
}
