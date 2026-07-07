package fs

import gosync "sync"

// writeFeedback owns the .error / .last state of every writable surface: the
// last failed-write message per entity (surfaced at that entity's .error file)
// and the recent successful creates per collection (surfaced at .last). Both
// were four loose fields on the LinearFS god-object while their accessors lived
// two files away; gathering the maps, their mutexes, and the accessors into one
// embedded value keeps the state and the behavior that guards it together.
//
// Its only dependency on the rest of the mount is the invalidate seam — the
// kernel-cache drop after a change — so it is exercised in isolation with a
// recording func (see successfile_test.go). LinearFS embeds one, so
// lfs.SetWriteError / lfs.AppendWriteSuccess / … keep working by promotion.
type writeFeedback struct {
	// invalidate drops the kernel's cached size/content for a virtual file's
	// inode so the next stat/read reflects a just-changed .error/.last. Never
	// nil (defaulted to a no-op by newWriteFeedback).
	invalidate func(ino uint64)

	// errors holds the last failed-write message per entity ID.
	errorsMu gosync.RWMutex
	errors   map[string]*WriteError

	// successes holds recent creates per collection key (capped, newest-last).
	successesMu gosync.RWMutex
	successes   map[string][]*WriteResult
}

// newWriteFeedback builds an initialized feedback store. invalidate is the
// kernel-cache drop applied after every change; a nil invalidate degrades to a
// no-op so a bare test store needs no server.
func newWriteFeedback(invalidate func(ino uint64)) writeFeedback {
	if invalidate == nil {
		invalidate = func(uint64) {}
	}
	return writeFeedback{
		invalidate: invalidate,
		errors:     make(map[string]*WriteError),
		successes:  make(map[string][]*WriteResult),
	}
}
