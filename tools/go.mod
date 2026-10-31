// Developer and CI tools, kept out of the application module so their
// dependencies never join the app's module graph or raise its versions.
// Run from the repo root as: (cd tools && go tool govulncheck -C .. ./...)
module itchgrep/tools

go 1.27.3

tool golang.org/x/vuln/cmd/govulncheck

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)
