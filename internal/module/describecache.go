package module

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xibodev/compa/v4/pkg/modproto"
)

// describeKey identifies one version of an installed module: its binary's size
// and modification time, the digest its install recorded, and the identity it
// is expected to have. Any change describes the module again.
type describeKey struct {
	size     int64
	mod      time.Time
	digest   string
	moduleID string
}

type describeEntry struct {
	key describeKey
	res Result
}

var describeCache = struct {
	sync.Mutex
	m map[string]describeEntry
}{m: map[string]describeEntry{}}

// DescribeCached returns the module's descriptor, running the binary only when
// it changed since it was last described.
//
// Discovery described every module, one after another with a 10s budget each,
// on every Modules or Tools page load, every invocation from the cockpit and
// every artifact request. A describe is a process start; an unchanged binary
// answers the same way, so the answer is kept.
//
// Failures are not kept: a module that timed out once, while a virus scanner
// read it, should be asked again.
func (r *Runner) DescribeCached(ctx context.Context) (*modproto.Descriptor, *Result, error) {
	info, err := os.Stat(r.Binary)
	if err != nil {
		return r.Describe(ctx)
	}
	key := describeKey{size: info.Size(), mod: info.ModTime(), digest: r.Digest, moduleID: r.ModuleID}
	path, err := filepath.Abs(r.Binary)
	if err != nil {
		path = r.Binary
	}

	describeCache.Lock()
	entry, ok := describeCache.m[path]
	describeCache.Unlock()
	if ok && entry.key == key {
		// A fresh decode, so no caller shares a descriptor another can change.
		var d modproto.Descriptor
		if err := json.Unmarshal(entry.res.Envelope.Result, &d); err == nil {
			res := entry.res
			return &d, &res, nil
		}
	}

	d, res, err := r.Describe(ctx)
	if err != nil {
		return d, res, err
	}
	describeCache.Lock()
	describeCache.m[path] = describeEntry{key: key, res: *res}
	describeCache.Unlock()
	return d, res, nil
}

// InvalidateDescribeCache forgets every remembered descriptor. Install, Remove
// and enabling or disabling a module call it, so the next discovery describes
// what is actually there.
func InvalidateDescribeCache() {
	describeCache.Lock()
	clear(describeCache.m)
	describeCache.Unlock()
}
