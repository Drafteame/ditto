// Package depspin pins dependencies that will be used in upcoming milestones
// but are not yet imported by production code. Remove entries when the deps
// move to direct use.
package depspin

import (
	_ "github.com/bufbuild/protocompile"
	_ "google.golang.org/protobuf/proto"
	_ "nhooyr.io/websocket"
)
