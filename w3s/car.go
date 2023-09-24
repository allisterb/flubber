package w3s

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sync"

	"bytes"
	"encoding/binary"
	"errors"

	cbor "github.com/ipfs/go-ipld-cbor"
	mh "github.com/multiformats/go-multihash"

	blocks "github.com/ipfs/go-block-format"
	"github.com/ipfs/go-cid"
	format "github.com/ipfs/go-ipld-format"
)

type GetLinks func(context.Context, cid.Cid) ([]*format.Link, error)

func GetLinksWithDAG(ng format.NodeGetter) GetLinks {
	return func(ctx context.Context, c cid.Cid) ([]*format.Link, error) {
		return format.GetLinks(ctx, ng, c)
	}
}

// defaultConcurrentFetch is the default maximum number of concurrent fetches
// that 'fetchNodes' will start at a time
const defaultConcurrentFetch = 32

// walkOptions represent the parameters of a graph walking algorithm
type walkOptions struct {
	SkipRoot     bool
	Concurrency  int
	ErrorHandler func(c cid.Cid, err error) error
}

// WalkOption is a setter for walkOptions
type WalkOption func(*walkOptions)

func (wo *walkOptions) addHandler(handler func(c cid.Cid, err error) error) {
	if wo.ErrorHandler != nil {
		wo.ErrorHandler = func(c cid.Cid, err error) error {
			return handler(c, wo.ErrorHandler(c, err))
		}
	} else {
		wo.ErrorHandler = handler
	}
}

// SkipRoot is a WalkOption indicating that the root node should skipped
func SkipRoot() WalkOption {
	return func(walkOptions *walkOptions) {
		walkOptions.SkipRoot = true
	}
}

// Concurrent is a WalkOption indicating that node fetching should be done in
// parallel, with the default concurrency factor.
// NOTE: When using that option, the walk order is *not* guarantee.
// NOTE: It *does not* make multiple concurrent calls to the passed `visit` function.
func Concurrent() WalkOption {
	return func(walkOptions *walkOptions) {
		walkOptions.Concurrency = defaultConcurrentFetch
	}
}

// Concurrency is a WalkOption indicating that node fetching should be done in
// parallel, with a specific concurrency factor.
// NOTE: When using that option, the walk order is *not* guarantee.
// NOTE: It *does not* make multiple concurrent calls to the passed `visit` function.
func Concurrency(worker int) WalkOption {
	return func(walkOptions *walkOptions) {
		walkOptions.Concurrency = worker
	}
}

// IgnoreErrors is a WalkOption indicating that the walk should attempt to
// continue even when an error occur.
func IgnoreErrors() WalkOption {
	return func(walkOptions *walkOptions) {
		walkOptions.addHandler(func(c cid.Cid, err error) error {
			return nil
		})
	}
}

// IgnoreMissing is a WalkOption indicating that the walk should continue when
// a node is missing.
func IgnoreMissing() WalkOption {
	return func(walkOptions *walkOptions) {
		walkOptions.addHandler(func(c cid.Cid, err error) error {
			if format.IsNotFound(err) {
				return nil
			}
			return err
		})
	}
}

// OnMissing is a WalkOption adding a callback that will be triggered on a missing
// node.
func OnMissing(callback func(c cid.Cid)) WalkOption {
	return func(walkOptions *walkOptions) {
		walkOptions.addHandler(func(c cid.Cid, err error) error {
			if format.IsNotFound(err) {
				callback(c)
			}
			return err
		})
	}
}

// OnError is a WalkOption adding a custom error handler.
// If this handler return a nil error, the walk will continue.
func OnError(handler func(c cid.Cid, err error) error) WalkOption {
	return func(walkOptions *walkOptions) {
		walkOptions.addHandler(handler)
	}
}

// WalkGraph will walk the dag in order (depth first) starting at the given root.
func Walk(ctx context.Context, getLinks GetLinks, c cid.Cid, visit func(cid.Cid) bool, options ...WalkOption) error {
	visitDepth := func(c cid.Cid, depth int) bool {
		return visit(c)
	}

	return WalkDepth(ctx, getLinks, c, visitDepth, options...)
}

// WalkDepth walks the dag starting at the given root and passes the current
// depth to a given visit function. The visit function can be used to limit DAG
// exploration.
func WalkDepth(ctx context.Context, getLinks GetLinks, c cid.Cid, visit func(cid.Cid, int) bool, options ...WalkOption) error {
	opts := &walkOptions{}
	for _, opt := range options {
		opt(opts)
	}

	if opts.Concurrency > 1 {
		return parallelWalkDepth(ctx, getLinks, c, visit, opts)
	} else {
		return sequentialWalkDepth(ctx, getLinks, c, 0, visit, opts)
	}
}

func sequentialWalkDepth(ctx context.Context, getLinks GetLinks, root cid.Cid, depth int, visit func(cid.Cid, int) bool, options *walkOptions) error {
	if !(options.SkipRoot && depth == 0) {
		if !visit(root, depth) {
			return nil
		}
	}

	links, err := getLinks(ctx, root)
	if err != nil && options.ErrorHandler != nil {
		err = options.ErrorHandler(root, err)
	}
	if err != nil {
		return err
	}

	for _, lnk := range links {
		if err := sequentialWalkDepth(ctx, getLinks, lnk.Cid, depth+1, visit, options); err != nil {
			return err
		}
	}
	return nil
}

func parallelWalkDepth(ctx context.Context, getLinks GetLinks, root cid.Cid, visit func(cid.Cid, int) bool, options *walkOptions) error {
	type cidDepth struct {
		cid   cid.Cid
		depth int
	}

	type linksDepth struct {
		links []*format.Link
		depth int
	}

	feed := make(chan cidDepth)
	out := make(chan linksDepth)
	done := make(chan struct{})

	var visitlk sync.Mutex
	var wg sync.WaitGroup

	errChan := make(chan error)
	fetchersCtx, cancel := context.WithCancel(ctx)
	defer wg.Wait()
	defer cancel()
	for i := 0; i < options.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cdepth := range feed {
				ci := cdepth.cid
				depth := cdepth.depth

				var shouldVisit bool

				// bypass the root if needed
				if !(options.SkipRoot && depth == 0) {
					visitlk.Lock()
					shouldVisit = visit(ci, depth)
					visitlk.Unlock()
				} else {
					shouldVisit = true
				}

				if shouldVisit {
					links, err := getLinks(ctx, ci)
					if err != nil && options.ErrorHandler != nil {
						err = options.ErrorHandler(root, err)
					}
					if err != nil {
						select {
						case errChan <- err:
						case <-fetchersCtx.Done():
						}
						return
					}

					outLinks := linksDepth{
						links: links,
						depth: depth + 1,
					}

					select {
					case out <- outLinks:
					case <-fetchersCtx.Done():
						return
					}
				}
				select {
				case done <- struct{}{}:
				case <-fetchersCtx.Done():
				}
			}
		}()
	}
	defer close(feed)

	send := feed
	var todoQueue []cidDepth
	var inProgress int

	next := cidDepth{
		cid:   root,
		depth: 0,
	}

	for {
		select {
		case send <- next:
			inProgress++
			if len(todoQueue) > 0 {
				next = todoQueue[0]
				todoQueue = todoQueue[1:]
			} else {
				next = cidDepth{}
				send = nil
			}
		case <-done:
			inProgress--
			if inProgress == 0 && !next.cid.Defined() {
				return nil
			}
		case linksDepth := <-out:
			for _, lnk := range linksDepth.links {
				cd := cidDepth{
					cid:   lnk.Cid,
					depth: linksDepth.depth,
				}

				if !next.cid.Defined() {
					next = cd
					send = feed
				} else {
					todoQueue = append(todoQueue, cd)
				}
			}
		case err := <-errChan:
			return err

		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func init() {
	cbor.RegisterCborType(CarHeader{})
}

type Store interface {
	Put(context.Context, blocks.Block) error
}

type ReadStore interface {
	Get(context.Context, cid.Cid) (blocks.Block, error)
}

type CarHeader struct {
	Roots   []cid.Cid
	Version uint64
}

type carWriter struct {
	ds   format.NodeGetter
	w    io.Writer
	walk WalkFunc
}

type WalkFunc func(format.Node) ([]*format.Link, error)

func WriteCar(ctx context.Context, ds format.NodeGetter, roots []cid.Cid, w io.Writer, options ...WalkOption) error {
	return WriteCarWithWalker(ctx, ds, roots, w, DefaultWalkFunc, options...)
}

func WriteCarWithWalker(ctx context.Context, ds format.NodeGetter, roots []cid.Cid, w io.Writer, walk WalkFunc, options ...WalkOption) error {

	h := &CarHeader{
		Roots:   roots,
		Version: 1,
	}

	if err := WriteHeader(h, w); err != nil {
		return fmt.Errorf("failed to write car header: %s", err)
	}

	cw := &carWriter{ds: ds, w: w, walk: walk}
	seen := cid.NewSet()
	for _, r := range roots {
		if err := Walk(ctx, cw.enumGetLinks, r, seen.Visit, options...); err != nil {
			return err
		}
	}
	return nil
}

func DefaultWalkFunc(nd format.Node) ([]*format.Link, error) {
	return nd.Links(), nil
}

func ReadHeader(br *bufio.Reader) (*CarHeader, error) {
	hb, err := LdRead(br)
	if err != nil {
		return nil, err
	}

	var ch CarHeader
	if err := cbor.DecodeInto(hb, &ch); err != nil {
		return nil, fmt.Errorf("invalid header: %v", err)
	}

	return &ch, nil
}

func WriteHeader(h *CarHeader, w io.Writer) error {
	hb, err := cbor.DumpObject(h)
	if err != nil {
		return err
	}

	return LdWrite(w, hb)
}

func HeaderSize(h *CarHeader) (uint64, error) {
	hb, err := cbor.DumpObject(h)
	if err != nil {
		return 0, err
	}

	return LdSize(hb), nil
}

func (cw *carWriter) enumGetLinks(ctx context.Context, c cid.Cid) ([]*format.Link, error) {
	nd, err := cw.ds.Get(ctx, c)
	if err != nil {
		return nil, err
	}

	if err := cw.writeNode(ctx, nd); err != nil {
		return nil, err
	}

	return cw.walk(nd)
}

func (cw *carWriter) writeNode(ctx context.Context, nd format.Node) error {
	return LdWrite(cw.w, nd.Cid().Bytes(), nd.RawData())
}

var bufioReaderPool = sync.Pool{
	New: func() any { return bufio.NewReader(nil) },
}

type CarReader struct {
	br     *bufio.Reader // set nil on EOF
	Header *CarHeader
}

func NewCarReader(r io.Reader) (*CarReader, error) {
	br := bufioReaderPool.Get().(*bufio.Reader)
	br.Reset(r)
	ch, err := ReadHeader(br)
	if err != nil {
		bufioReaderPool.Put(br)
		return nil, err
	}

	if ch.Version != 1 {
		return nil, fmt.Errorf("invalid car version: %d", ch.Version)
	}

	if len(ch.Roots) == 0 {
		return nil, fmt.Errorf("empty car, no roots")
	}

	return &CarReader{
		br:     br,
		Header: ch,
	}, nil
}

func (cr *CarReader) Next() (blocks.Block, error) {
	if cr.br == nil {
		return nil, io.EOF
	}
	c, data, err := ReadNode(cr.br)
	if err != nil {
		if err == io.EOF {
			// Common happy case: recycle the bufio.Reader.
			// In the other error paths leaking it is fine.
			bufioReaderPool.Put(cr.br)
			cr.br = nil
		}
		return nil, err
	}

	hashed, err := c.Prefix().Sum(data)
	if err != nil {
		return nil, err
	}

	if !hashed.Equals(c) {
		return nil, fmt.Errorf("mismatch in content integrity, name: %s, data: %s", c, hashed)
	}

	return blocks.NewBlockWithCid(data, c)
}

type batchStore interface {
	PutMany(context.Context, []blocks.Block) error
}

func LoadCar(ctx context.Context, s Store, r io.Reader) (*CarHeader, error) {
	cr, err := NewCarReader(r)
	if err != nil {
		return nil, err
	}

	if bs, ok := s.(batchStore); ok {
		return loadCarFast(ctx, bs, cr)
	}

	return loadCarSlow(ctx, s, cr)
}

func loadCarFast(ctx context.Context, s batchStore, cr *CarReader) (*CarHeader, error) {
	var buf []blocks.Block
	for {
		blk, err := cr.Next()
		if err != nil {
			if err == io.EOF {
				if len(buf) > 0 {
					if err := s.PutMany(ctx, buf); err != nil {
						return nil, err
					}
				}
				return cr.Header, nil
			}
			return nil, err
		}

		buf = append(buf, blk)

		if len(buf) > 1000 {
			if err := s.PutMany(ctx, buf); err != nil {
				return nil, err
			}
			buf = buf[:0]
		}
	}
}

func loadCarSlow(ctx context.Context, s Store, cr *CarReader) (*CarHeader, error) {
	for {
		blk, err := cr.Next()
		if err != nil {
			if err == io.EOF {
				return cr.Header, nil
			}
			return nil, err
		}

		if err := s.Put(ctx, blk); err != nil {
			return nil, err
		}
	}
}

// MaxAllowedSectionSize dictates the maximum number of bytes that a CARv1 header
// or section is allowed to occupy without causing a decode to error.
// This cannot be supplied as an option, only adjusted as a global. You should
// use v2#NewReader instead since it allows for options to be passed in.
var MaxAllowedSectionSize uint = 32 << 20 // 32MiB

var cidv0Pref = []byte{0x12, 0x20}

type BytesReader interface {
	io.Reader
	io.ByteReader
}

// Deprecated: ReadCid shouldn't be used directly, use CidFromReader from go-cid
func ReadCid(buf []byte) (cid.Cid, int, error) {
	if len(buf) >= 2 && bytes.Equal(buf[:2], cidv0Pref) {
		i := 34
		if len(buf) < i {
			i = len(buf)
		}
		c, err := cid.Cast(buf[:i])
		return c, i, err
	}

	br := bytes.NewReader(buf)

	// assume cidv1
	vers, err := binary.ReadUvarint(br)
	if err != nil {
		return cid.Cid{}, 0, err
	}

	// TODO: the go-cid package allows version 0 here as well
	if vers != 1 {
		return cid.Cid{}, 0, fmt.Errorf("invalid cid version number")
	}

	codec, err := binary.ReadUvarint(br)
	if err != nil {
		return cid.Cid{}, 0, err
	}

	mhr := mh.NewReader(br)
	h, err := mhr.ReadMultihash()
	if err != nil {
		return cid.Cid{}, 0, err
	}

	return cid.NewCidV1(codec, h), len(buf) - br.Len(), nil
}

func ReadNode(br *bufio.Reader) (cid.Cid, []byte, error) {
	data, err := LdRead(br)
	if err != nil {
		return cid.Cid{}, nil, err
	}

	n, c, err := cid.CidFromReader(bytes.NewReader(data))
	if err != nil {
		return cid.Cid{}, nil, err
	}

	return c, data[n:], nil
}

func LdWrite(w io.Writer, d ...[]byte) error {
	var sum uint64
	for _, s := range d {
		sum += uint64(len(s))
	}

	buf := make([]byte, 8)
	n := binary.PutUvarint(buf, sum)
	_, err := w.Write(buf[:n])
	if err != nil {
		return err
	}

	for _, s := range d {
		_, err = w.Write(s)
		if err != nil {
			return err
		}
	}

	return nil
}

func LdSize(d ...[]byte) uint64 {
	var sum uint64
	for _, s := range d {
		sum += uint64(len(s))
	}
	buf := make([]byte, 8)
	n := binary.PutUvarint(buf, sum)
	return sum + uint64(n)
}

func LdRead(r *bufio.Reader) ([]byte, error) {
	if _, err := r.Peek(1); err != nil { // no more blocks, likely clean io.EOF
		return nil, err
	}

	l, err := binary.ReadUvarint(r)
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF // don't silently pretend this is a clean EOF
		}
		return nil, err
	}

	if l > uint64(MaxAllowedSectionSize) { // Don't OOM
		return nil, errors.New("malformed car; header is bigger than util.MaxAllowedSectionSize")
	}

	buf := make([]byte, l)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}

	return buf, nil
}
