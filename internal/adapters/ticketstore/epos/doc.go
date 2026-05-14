// Package epos adapts github.com/php-workx/epos to verk's ticketstore
// compatibility surface.
//
// Inside this package, alias external epos imports to avoid collision with this
// package name: eposticket, eposmarkdown, eposstore, eposruntime, and eposgraph.
package epos

import _ "github.com/php-workx/epos/ticket" // keep external epos module visible to adapter documentation tests
