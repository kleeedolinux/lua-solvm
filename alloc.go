package lua

import (
	"reflect"
	"sync"
	"unsafe"
)

// iface is an internal representation of the go-interface.
type iface struct {
	itab unsafe.Pointer
	word unsafe.Pointer
}

const (
	preloadLimit LNumber = 128
	allocSize    int     = 1024
)

var (
	preloads  [int(preloadLimit)]LValue
	allocPool sync.Pool
)

func init() {
	for i := 0; i < int(preloadLimit); i++ {
		preloads[i] = LNumber(i)
	}

	allocPool = sync.Pool{
		New: func() interface{} {
			return newAllocator(allocSize)
		},
	}
}

// allocator is a fast bulk memory allocator for the LValue.
type allocator struct {
	size    int
	fptrs   []float64
	fheader *reflect.SliceHeader

	scratchValue  LValue
	scratchValueP *iface

	pool *sync.Pool
}

func newAllocator(size int) *allocator {
	al := &allocator{
		size:    size,
		fptrs:   make([]float64, 0, size),
		fheader: nil,
		pool:    &allocPool,
	}
	al.fheader = (*reflect.SliceHeader)(unsafe.Pointer(&al.fptrs))
	al.scratchValue = LNumber(0)
	al.scratchValueP = (*iface)(unsafe.Pointer(&al.scratchValue))

	return al
}

// LNumber2I takes a number value and returns an interface LValue representing the same number.
// Converting an LNumber to a LValue naively, by doing:
// `var val LValue = myLNumber`
// will result in an individual heap alloc of 8 bytes for the float value. LNumber2I amortizes the cost and memory
// overhead of these allocs by allocating blocks of floats instead.
// The downside of this is that all of the floats on a given block have to become eligible for gc before the block
// as a whole can be gc-ed.
func (al *allocator) LNumber2I(v LNumber) LValue {
	if v >= 0 && v < preloadLimit && float64(v) == float64(int64(v)) {
		return preloads[int(v)]
	}

	if cap(al.fptrs) == len(al.fptrs) {
		if len(al.fptrs) >= allocSize*2 {
			al.fptrs = make([]float64, 0, allocSize)
		} else {
			al.fptrs = make([]float64, 0, cap(al.fptrs)*2)
		}
		al.fheader = (*reflect.SliceHeader)(unsafe.Pointer(&al.fptrs))
	}

	al.fptrs = append(al.fptrs, float64(v))
	fptr := &al.fptrs[len(al.fptrs)-1]
	al.scratchValueP.word = unsafe.Pointer(fptr)

	return al.scratchValue
}

func (al *allocator) Reset() {
	if al != nil {
		al.fptrs = al.fptrs[:0]
	}
}

func (al *allocator) Release() {
	if al != nil {
		al.Reset()
		al.pool.Put(al)
	}
}

func GetAllocator() *allocator {
	return allocPool.Get().(*allocator)
}
