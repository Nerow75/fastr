//go:build !windows

package trust

// restrictToOwner is a no-op where the permission bits already say it.
//
// On a POSIX system the 0700 on the directory and the 0600 on every key *are*
// the restriction, enforced by the kernel at every open. There is nothing
// further to set here, and a second mechanism doing the same job would only be
// one more thing to keep in agreement with the first.
func restrictToOwner(string) error { return nil }
