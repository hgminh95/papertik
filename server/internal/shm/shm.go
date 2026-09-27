// Package shm is the client side of the shared-memory protocol served by vecdb.
// The layout mirrors vecdb/src/shm.rs; keep them in sync.
package shm

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	magic          = uint64(0x31304d48534b5450) // "PTKSHM01" little endian
	version        = 1
	headerSize     = 128
	slotHeaderSize = 64

	stateFree      = 0
	stateClaimed   = 1
	stateReady     = 2
	stateBusy      = 3
	stateDone      = 4
	stateAbandoned = 5

	opSearch = 1

	statusOK = 0

	// vecdb bumps the heartbeat every 100ms; older than this means it is gone.
	staleAfter = 2 * time.Second
)

var (
	ErrUnavailable = errors.New("vecdb unavailable")
	ErrBadRequest  = errors.New("vecdb rejected request")
)

type Hit struct {
	Row   uint32
	Score float32
}

type region struct {
	mem        []byte
	numSlots   int
	dim        int
	maxK       int
	maxExclude int
	slotSize   int
	indexN     int
	next       atomic.Uint32
}

func openRegion(path string) (*region, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() < headerSize {
		return nil, fmt.Errorf("%s: too small", path)
	}
	mem, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", path, err)
	}
	r := &region{mem: mem}
	if atomic.LoadUint64(r.u64(0)) != magic {
		syscall.Munmap(mem)
		return nil, fmt.Errorf("%s: not initialised", path)
	}
	le := binary.LittleEndian
	if v := le.Uint32(mem[8:]); v != version {
		syscall.Munmap(mem)
		return nil, fmt.Errorf("%s: version %d, want %d", path, v, version)
	}
	r.numSlots = int(le.Uint32(mem[12:]))
	r.dim = int(le.Uint32(mem[16:]))
	r.maxK = int(le.Uint32(mem[20:]))
	r.maxExclude = int(le.Uint32(mem[24:]))
	r.slotSize = int(le.Uint32(mem[28:]))
	r.indexN = int(le.Uint32(mem[44:]))
	if headerSize+r.numSlots*r.slotSize > len(mem) {
		syscall.Munmap(mem)
		return nil, fmt.Errorf("%s: truncated", path)
	}
	return r, nil
}

func (r *region) u32(off int) *uint32 { return (*uint32)(unsafe.Pointer(&r.mem[off])) }
func (r *region) u64(off int) *uint64 { return (*uint64)(unsafe.Pointer(&r.mem[off])) }

func (r *region) alive() bool {
	hb := int64(atomic.LoadUint64(r.u64(32)))
	return time.Since(time.UnixMilli(hb)) < staleAfter
}

func (r *region) slot(i int) int { return headerSize + i*r.slotSize }

// Client talks to vecdb. It reconnects transparently when vecdb restarts.
type Client struct {
	path string
	mu   sync.Mutex
	r    *region
}

func NewClient(path string) *Client { return &Client{path: path} }

// get returns a live region, (re)opening the file if needed.
func (c *Client) get() (*region, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.r != nil && c.r.alive() {
		return c.r, nil
	}
	r, err := openRegion(c.path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !r.alive() {
		syscall.Munmap(r.mem)
		return nil, fmt.Errorf("%w: stale heartbeat", ErrUnavailable)
	}
	// The previous mapping may still be referenced by in-flight requests; it is small, so
	// it is intentionally leaked rather than unmapped under them.
	c.r = r
	return r, nil
}

// Alive reports whether vecdb is up.
func (c *Client) Alive() bool {
	_, err := c.get()
	return err == nil
}

// Search asks vecdb for the top-k rows by inner product with q, skipping rows in exclude.
func (c *Client) Search(ctx context.Context, q []float32, k int, exclude []uint32) ([]Hit, error) {
	r, err := c.get()
	if err != nil {
		return nil, err
	}
	if len(q) != r.dim {
		return nil, fmt.Errorf("query dim %d, index dim %d", len(q), r.dim)
	}
	k = min(k, r.maxK)
	if len(exclude) > r.maxExclude {
		exclude = exclude[len(exclude)-r.maxExclude:] // keep the most recent
	}

	base, err := r.claim(ctx)
	if err != nil {
		return nil, err
	}
	state := r.u32(base)

	le := binary.LittleEndian
	le.PutUint32(r.mem[base+4:], opSearch)
	le.PutUint32(r.mem[base+8:], uint32(k))
	le.PutUint32(r.mem[base+12:], uint32(len(exclude)))
	copy(unsafe.Slice((*float32)(unsafe.Pointer(&r.mem[base+slotHeaderSize])), r.dim), q)
	offExclude := base + slotHeaderSize + 4*r.dim
	if len(exclude) > 0 {
		copy(unsafe.Slice((*uint32)(unsafe.Pointer(&r.mem[offExclude])), len(exclude)), exclude)
	}
	atomic.StoreUint32(state, stateReady) // publishes the payload

	if err := r.wait(ctx, state); err != nil {
		// Give the slot back without racing vecdb.
		if atomic.CompareAndSwapUint32(state, stateReady, stateFree) ||
			atomic.CompareAndSwapUint32(state, stateBusy, stateAbandoned) {
			return nil, err
		}
		// It completed in the meantime; fall through and use the answer.
	}

	status := le.Uint32(r.mem[base+16:])
	n := int(le.Uint32(r.mem[base+20:]))
	var hits []Hit
	if status == statusOK {
		offRows := offExclude + 4*r.maxExclude
		offScores := offRows + 4*r.maxK
		rows := unsafe.Slice((*uint32)(unsafe.Pointer(&r.mem[offRows])), n)
		scores := unsafe.Slice((*float32)(unsafe.Pointer(&r.mem[offScores])), n)
		hits = make([]Hit, n)
		for i := range hits {
			hits[i] = Hit{Row: rows[i], Score: scores[i]}
		}
	}
	atomic.StoreUint32(state, stateFree)
	if status != statusOK {
		return nil, ErrBadRequest
	}
	return hits, nil
}

// claim finds a FREE slot and moves it to CLAIMED, returning its offset.
func (r *region) claim(ctx context.Context) (int, error) {
	for attempt := 0; ; attempt++ {
		start := int(r.next.Add(1))
		for i := 0; i < r.numSlots; i++ {
			off := r.slot((start + i) % r.numSlots)
			if atomic.CompareAndSwapUint32(r.u32(off), stateFree, stateClaimed) {
				return off, nil
			}
		}
		if ctx.Err() != nil {
			return 0, fmt.Errorf("%w: all slots busy", ErrUnavailable)
		}
		time.Sleep(time.Duration(min(attempt+1, 50)) * 20 * time.Microsecond)
	}
}

// wait polls until the slot is DONE, with back-off: yield first, then short sleeps.
// It gives up early if vecdb stops heartbeating.
func (r *region) wait(ctx context.Context, state *uint32) error {
	for i := 0; ; i++ {
		if atomic.LoadUint32(state) == stateDone {
			return nil
		}
		if i < 64 {
			runtime.Gosched()
			continue
		}
		if i%16 == 0 {
			if ctx.Err() != nil {
				return fmt.Errorf("%w: %v", ErrUnavailable, ctx.Err())
			}
			if !r.alive() {
				return fmt.Errorf("%w: vecdb stopped", ErrUnavailable)
			}
		}
		d := time.Duration(min(i-63, 25)) * 20 * time.Microsecond // up to 500µs
		time.Sleep(d)
	}
}
