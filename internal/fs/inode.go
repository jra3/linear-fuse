package fs

import "hash/fnv"

// ino derives a stable FUSE inode number for an entity from a namespace and an
// id. The namespace keeps inodes for different entity kinds disjoint even when
// their ids coincide (e.g. an issue and the comments dir under it), which the
// per-kind helpers below rely on for uniqueness — see TestInoNamespacesDistinct.
//
// Inode numbers live only in memory (go-fuse regenerates them each mount and
// never persists them), so the exact hash is an implementation detail free to
// change; only its stability within a single mount matters.
func ino(namespace, id string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(namespace + ":" + id))
	return h.Sum64()
}
