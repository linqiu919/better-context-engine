// Package version carries the build version shown in the console. The
// default applies to local builds; release images override it at build time
// via -ldflags "-X ...version.Version=$TAG" with the pushed git tag, so the
// displayed version follows every release without code changes.
package version

var Version = "v0.0.1"
