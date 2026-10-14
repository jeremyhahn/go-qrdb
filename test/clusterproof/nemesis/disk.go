//go:build clusterproof

package nemesis

import (
	"path"
	"strconv"
	"strings"
)

// DefaultDiskFill fills the filesystem holding the directory it is given.
// fallocate takes what df reports available, then dd writes until ENOSPC,
// because a root-owned server can still write into the blocks df keeps in
// reserve. It runs in a fault container that mounts the node's data volume.
var DefaultDiskFill = []string{"sh", "-c", `f="$1/.qrdb-nemesis-fill"
avail=$(df -Pk "$1" | awk 'NR==2 {print $4}')
fallocate -l "$((avail * 1024))" "$f" 2>/dev/null || true
dd if=/dev/zero of="$f.tail" bs=64k 2>/dev/null || true
test -e "$f" || test -e "$f.tail"`, "nemesis-disk-full", ArgvElement}

// DefaultDiskFree removes what DefaultDiskFill wrote; rm -f makes it
// idempotent.
var DefaultDiskFree = []string{"sh", "-c", `rm -f "$1/.qrdb-nemesis-fill" "$1/.qrdb-nemesis-fill.tail"`, "nemesis-disk-full", ArgvElement}

// DiskFullConfig configures the disk-full fault.
type DiskFullConfig struct {
	// Path is the node's data directory, as the fault command sees it.
	Path string
	// Fill and Free are the inject and heal templates, rendered with .Node
	// and .Path and with ArgvElement expanding to the path; nil is
	// DefaultDiskFill and DefaultDiskFree.
	Fill []string
	Free []string
}

type diskFullData struct {
	Node string
	Path string
}

// NewDiskFull returns the fault that fills each target's data filesystem and
// frees it again.
func NewDiskFull(cfg DiskFullConfig) (Nemesis, error) {
	if err := checkAbsolute("disk-full path", cfg.Path); err != nil {
		return nil, err
	}
	data := func(t Target) (any, []string) {
		return diskFullData{Node: t.Name(), Path: cfg.Path}, []string{cfg.Path}
	}
	fill, free, err := templatePair("disk-full", cfg.Fill, DefaultDiskFill, cfg.Free, DefaultDiskFree, diskFullData{Node: "node", Path: cfg.Path})
	if err != nil {
		return nil, err
	}
	return &fault{name: "disk-full", plan: staticPlan(execStep(fill, data)), heal: execStep(free, data)}, nil
}

// DefaultDiskThrottle caps the node's block I/O through the cgroup v2
// io.max file of the server's cgroup: arguments device, bytes per second,
// cgroup directory.
var DefaultDiskThrottle = []string{"sh", "-c", `echo "$1 rbps=$2 wbps=$2" > "$3/io.max"`, "nemesis-slow-disk", ArgvElement}

// DefaultDiskUnthrottle lifts every io.max limit on the device; writing
// "max" twice is the same as once.
var DefaultDiskUnthrottle = []string{"sh", "-c", `echo "$1 rbps=max wbps=max riops=max wiops=max" > "$3/io.max"`, "nemesis-slow-disk", ArgvElement}

// SlowDiskConfig configures the slow-disk fault.
type SlowDiskConfig struct {
	// Device is the MAJ:MIN of the block device holding the data directory.
	Device string
	// Cgroup is the server's cgroup v2 directory, as the fault command sees
	// it.
	Cgroup string
	// BytesPerSecond caps reads and writes.
	BytesPerSecond uint64
	// Throttle and Unthrottle are the inject and heal templates, rendered
	// with .Node, .Device, .Cgroup and .BytesPerSecond and with ArgvElement
	// expanding to device, rate and cgroup; nil is DefaultDiskThrottle and
	// DefaultDiskUnthrottle.
	Throttle   []string
	Unthrottle []string
}

type slowDiskData struct {
	Node           string
	Device         string
	Cgroup         string
	BytesPerSecond uint64
}

// NewSlowDisk returns the fault that throttles each target's block I/O and
// lifts the throttle again.
func NewSlowDisk(cfg SlowDiskConfig) (Nemesis, error) {
	if !isDevice(cfg.Device) {
		return nil, &ConfigError{Field: "slow-disk device", Reason: "must be MAJ:MIN, got " + strconv.Quote(cfg.Device)}
	}
	if err := checkAbsolute("slow-disk cgroup", cfg.Cgroup); err != nil {
		return nil, err
	}
	if cfg.BytesPerSecond == 0 {
		return nil, &ConfigError{Field: "slow-disk rate", Reason: "must be positive"}
	}
	inner := []string{cfg.Device, strconv.FormatUint(cfg.BytesPerSecond, 10), cfg.Cgroup}
	sample := slowDiskData{Node: "node", Device: cfg.Device, Cgroup: cfg.Cgroup, BytesPerSecond: cfg.BytesPerSecond}
	data := func(t Target) (any, []string) {
		d := sample
		d.Node = t.Name()
		return d, inner
	}
	throttle, unthrottle, err := templatePair("slow-disk", cfg.Throttle, DefaultDiskThrottle, cfg.Unthrottle, DefaultDiskUnthrottle, sample)
	if err != nil {
		return nil, err
	}
	return &fault{name: "slow-disk", plan: staticPlan(execStep(throttle, data)), heal: execStep(unthrottle, data)}, nil
}

// checkAbsolute requires p to be a clean absolute POSIX path: it names a
// directory on the node, not on this host.
func checkAbsolute(field, p string) error {
	if !path.IsAbs(p) || path.Clean(p) != p {
		return &ConfigError{Field: field, Reason: "must be a clean absolute path, got " + strconv.Quote(p)}
	}
	return nil
}

// isDevice reports whether s is a block device number, MAJ:MIN.
func isDevice(s string) bool {
	major, minor, ok := strings.Cut(s, ":")
	if !ok {
		return false
	}
	_, errMajor := strconv.ParseUint(major, 10, 32)
	_, errMinor := strconv.ParseUint(minor, 10, 32)
	return errMajor == nil && errMinor == nil
}
