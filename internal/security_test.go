package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"

	rootsv1 "github.com/Muxcore-Media/media-root-folders/proto/rootsv1"
)

func newConfinedModule(t *testing.T, cfg Config) *Module {
	t.Helper()
	cfg.DBPath = filepath.Join(t.TempDir(), "roots.db")
	cfg.GRPCAddr = ":0"
	m := NewModule(cfg)
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	return m
}

// layout: base/lib (allowed), base/lib2 (sibling prefix), outside (elsewhere),
// lib/escape -> outside (symlink).
func confinementLayout(t *testing.T) (lib, sibling, outside, escape string) {
	t.Helper()
	base := t.TempDir()
	lib = mustTempDir(t, base, "lib")
	sibling = mustTempDir(t, base, "lib2")
	outside = t.TempDir()
	escape = filepath.Join(lib, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}
	return lib, sibling, outside, escape
}

func TestRootWritesFailClosedWithoutAllowList(t *testing.T) {
	t.Setenv("ROOTS_ALLOWED_PREFIXES", "")
	m := newConfinedModule(t, Config{})
	ctx := context.Background()
	dir := t.TempDir()
	_, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: dir})
	expectCode(t, err, codes.FailedPrecondition)
	_, err = m.SeedFromPaths(ctx, &rootsv1.SeedFromPathsRequest{Paths: []string{dir}})
	expectCode(t, err, codes.FailedPrecondition)
	_, err = m.ProbeRoot(ctx, &rootsv1.ProbeRootRequest{Path: dir})
	expectCode(t, err, codes.FailedPrecondition)
	// No browse prefixes, no allow-list, no registered roots: browse refused
	// (the old default was the whole filesystem).
	_, err = m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: "/"})
	expectCode(t, err, codes.FailedPrecondition)
	_, err = m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{})
	expectCode(t, err, codes.FailedPrecondition)
}

func TestRootPathConfinement(t *testing.T) {
	lib, sibling, outside, escape := confinementLayout(t)
	m := newConfinedModule(t, Config{AllowedPrefixes: []string{lib}})
	ctx := context.Background()
	movies := mustTempDir(t, lib, "movies")

	bad := map[string]string{
		"root fs":        "/",
		"etc":            "/etc",
		"outside":        outside,
		"sibling-prefix": sibling,
		"symlink escape": escape,
		"dotdot":         lib + "/../lib2",
	}
	for name, p := range bad {
		_, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: p})
		expectCode(t, err, codes.InvalidArgument)
		_, err = m.SeedFromPaths(ctx, &rootsv1.SeedFromPathsRequest{Paths: []string{p}, SkipValidation: true})
		expectCode(t, err, codes.InvalidArgument)
		_, err = m.ProbeRoot(ctx, &rootsv1.ProbeRootRequest{Path: p})
		expectCode(t, err, codes.InvalidArgument)
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}

	created, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: movies})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.UpdateRoot(ctx, &rootsv1.UpdateRootRequest{Id: created.Root.Id, Path: escape})
	expectCode(t, err, codes.InvalidArgument)
	_, err = m.UpdateRoot(ctx, &rootsv1.UpdateRootRequest{Id: created.Root.Id, Path: "/etc"})
	expectCode(t, err, codes.InvalidArgument)
}

func TestBrowsePathConfinement(t *testing.T) {
	lib, sibling, outside, escape := confinementLayout(t)
	_ = mustTempDir(t, lib, "movies")
	m := newConfinedModule(t, Config{AllowedPrefixes: []string{lib}})
	ctx := context.Background()

	for name, p := range map[string]string{
		"root fs": "/", "outside": outside, "sibling-prefix": sibling,
		"symlink escape": escape, "dotdot": lib + "/../lib2",
	} {
		_, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: p})
		expectCode(t, err, codes.InvalidArgument)
		if t.Failed() {
			t.Fatalf("case %s", name)
		}
	}

	// Empty path with one browse root starts there; the symlinked directory
	// is not offered and there is no parent above the root.
	resp, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Path != lib || resp.Parent != "" {
		t.Fatalf("path=%q parent=%q", resp.Path, resp.Parent)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Name != "movies" {
		t.Fatalf("entries: %+v", resp.Entries)
	}
}

func TestBrowseDefaultsToRegisteredRoots(t *testing.T) {
	lib, sibling, _, _ := confinementLayout(t)
	m := newConfinedModule(t, Config{AllowedPrefixes: []string{lib}})
	ctx := context.Background()
	movies := mustTempDir(t, lib, "movies")
	tv := mustTempDir(t, lib, "tv")
	for _, p := range []string{movies, tv} {
		if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: p}); err != nil {
			t.Fatal(err)
		}
	}
	// Drop the allow-list: browse falls back to the registered roots only.
	m.allowedPrefixes = nil
	resp, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries: %+v", resp.Entries)
	}
	if _, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: movies}); err != nil {
		t.Fatal(err)
	}
	_, err = m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: lib})
	expectCode(t, err, codes.InvalidArgument)
	_, err = m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: sibling})
	expectCode(t, err, codes.InvalidArgument)
}

func TestLegacyRootOutsideAllowListNotPicked(t *testing.T) {
	lib, _, outside, _ := confinementLayout(t)
	m := newConfinedModule(t, Config{AllowedPrefixes: []string{lib, outside}})
	ctx := context.Background()
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: outside, MediaKind: "movies"}); err != nil {
		t.Fatal(err)
	}
	// Operator narrows the allow-list; the old root stays listed but is
	// reported inaccessible and never picked.
	m.allowedPrefixes = []string{lib}
	list, err := m.ListRoots(ctx, &rootsv1.ListRootsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Roots) != 1 || list.Roots[0].Accessible {
		t.Fatalf("roots: %+v", list.Roots)
	}
	_, err = m.PickRoot(ctx, &rootsv1.PickRootRequest{MediaKind: "movies"})
	expectCode(t, err, codes.NotFound)
}

func TestAllowedPrefixesFromEnv(t *testing.T) {
	t.Setenv("ROOTS_ALLOWED_PREFIXES", " /mnt/media/ , relative, ,/srv/library")
	t.Setenv("ROOTS_BROWSE_PREFIXES", "")
	m := NewModule(Config{})
	if len(m.allowedPrefixes) != 2 || m.allowedPrefixes[0] != "/mnt/media" || m.allowedPrefixes[1] != "/srv/library" {
		t.Fatalf("allowed=%v", m.allowedPrefixes)
	}
	if len(m.browsePrefixes) != 0 {
		t.Fatalf("browse=%v", m.browsePrefixes)
	}
}
