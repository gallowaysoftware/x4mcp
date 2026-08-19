package x4save

import "sync/atomic"

// The section mask exists for ONE reason: to answer "which of the new sections
// costs that" when the aggregate parse-cost gate goes red.
//
// The plan's S6 gate is "ParseMS ≤ +10%, peak RSS ≤ +5 MB". That says a
// regression happened; it does not say which of nine sections owns it, and a
// red that starts a bisect is a red that gets waived. The parser dispatches
// every section from one element-name switch with no options struct, so
// measuring a section alone needs a way to turn it off — this is it.
//
// It is read ONCE per parse into a local, so the walk itself pays nothing: no
// atomic load per element, and no data race between a benchmark flipping the
// mask and a parse already in flight (which -race would otherwise find).
//
// Production never touches it. Tests and benchmarks set it via withSections.
type sectionMask uint32

const (
	secLogbook sectionMask = 1 << iota
	secStats
	secMissions
	secLicences
	secInventory
	secThreat
	secHull
	secBuildStorage
	secResourceAreas

	secAll = sectionMask(1)<<iota - 1
)

// enabledSections is the mask every parse starts from. Nothing in the shipped
// paths writes it.
var enabledSections atomic.Uint32

func init() { enabledSections.Store(uint32(secAll)) }

// sectionNames drives the per-section cost benchmark's output and keeps the
// names it prints in the same place as the bits it flips.
var sectionNames = []struct {
	name string
	bit  sectionMask
}{
	{"logbook", secLogbook},
	{"stats", secStats},
	{"missions", secMissions},
	{"licences", secLicences},
	{"inventory", secInventory},
	{"threat", secThreat},
	{"hull", secHull},
	{"build_storage", secBuildStorage},
	{"resource_areas", secResourceAreas},
}

// withSections runs fn with only the given sections enabled, restoring the
// previous mask afterwards. Test/benchmark use only, and not safe to run
// concurrently with another parse — which is exactly why the parse reads the
// mask once and never again.
func withSections(m sectionMask, fn func()) {
	prev := enabledSections.Swap(uint32(m))
	defer enabledSections.Store(prev)
	fn()
}

func (m sectionMask) has(bit sectionMask) bool { return m&bit != 0 }
