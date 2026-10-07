// The server's commands: `everysaid serve` and `everysaid user` (everysaid/server), and the demo's
// --serve.
package main

import (
	"os"

	"everysaid/internal/demo"
	"everysaid/internal/server"
)

func init() {
	commands["serve"] = func(a []string) error { return server.ServeMain(a, os.Stdout) }
	commands["user"] = func(a []string) error { return server.UserMain(a, os.Stdout) }
	// the environment already points at the demo's folders: its archive, its port (8530)
	demo.Serve = func(dir string) error { return server.ServeMain(nil, os.Stdout) }
}
