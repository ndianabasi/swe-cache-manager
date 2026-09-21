// Package swecache exposes build-time resources for the swe-cache executable.
package swecache

import _ "embed"

// Readme is the documentation distributed with this binary.
//
//go:embed README.md
var Readme string
