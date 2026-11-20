//go:build !unix

package realdata

import "io/fs"

// Outside Unix the folder's owner is not checked; its mode and type still are.
func ownedByMe(fs.FileInfo) bool { return true }
