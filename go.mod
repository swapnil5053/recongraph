module github.com/swapnil5053/recongraph

go 1.24

require golang.org/x/net v0.29.0

// Build-environment workaround only: this build sandbox can reach github.com
// but not the golang.org vanity-import host. This maps the vanity path to its
// canonical GitHub source. Remove on a machine with normal network access.
replace golang.org/x/net => github.com/golang/net v0.29.0
