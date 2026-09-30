module github.com/goravaa/chotu/chotu-setup

go 1.25.0

require (
	github.com/goravaa/chotu/status v0.0.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.59.0
)

// The status document is defined in one place so the daemon and the status
// service cannot drift apart. The replace makes that work in a plain checkout,
// with no workspace file needed.
replace github.com/goravaa/chotu/status => ../status

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
