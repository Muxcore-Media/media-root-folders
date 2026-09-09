package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	rootsv1 "github.com/Muxcore-Media/media-root-folders/proto/rootsv1"
)

func newTestModule(t *testing.T) *Module {
	t.Helper()
	m := NewModule(Config{
		DBPath:   filepath.Join(t.TempDir(), "roots.db"),
		GRPCAddr: ":0",
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	return m
}

func mustTempDir(t *testing.T, parent, name string) string {
	t.Helper()
	dir := filepath.Join(parent, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func expectCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %v", code)
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != code {
		t.Fatalf("error %v: want code %v", err, code)
	}
}

func TestModuleInfo(t *testing.T) {
	m := NewModule(Config{})
	info := m.Info()
	if info.ID != "media-root-folders" {
		t.Errorf("id: %q", info.ID)
	}
	if len(info.Capabilities) == 0 || info.Capabilities[0] != "media.roots" {
		t.Errorf("capabilities: %v", info.Capabilities)
	}
}

func TestCreateListUpdateDelete(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	base := t.TempDir()
	movies := mustTempDir(t, base, "Movies")
	films := mustTempDir(t, base, "Films")

	created, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{
		Path:      movies,
		MediaKind: "movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Root.Path != movies {
		t.Errorf("path: %q", created.Root.Path)
	}
	if created.Root.Name != "Movies" {
		t.Errorf("default name: %q", created.Root.Name)
	}
	if created.Root.MediaKind != "movies" {
		t.Errorf("kind: %q", created.Root.MediaKind)
	}
	if !strings.HasPrefix(created.Root.Id, "rf_") {
		t.Errorf("id: %q", created.Root.Id)
	}
	if !created.Root.Accessible {
		t.Error("expected accessible")
	}
	if created.Root.FreeBytes <= 0 {
		t.Errorf("free_bytes: %d", created.Root.FreeBytes)
	}

	list, err := m.ListRoots(ctx, &rootsv1.ListRootsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Roots) != 1 {
		t.Fatalf("list len: %d", len(list.Roots))
	}

	updated, err := m.UpdateRoot(ctx, &rootsv1.UpdateRootRequest{
		Id:        created.Root.Id,
		Path:      films,
		Name:      "Films",
		MediaKind: "any",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Root.Path != films || updated.Root.Name != "Films" {
		t.Errorf("update: %+v", updated.Root)
	}

	_, err = m.DeleteRoot(ctx, &rootsv1.DeleteRootRequest{Id: created.Root.Id})
	if err != nil {
		t.Fatal(err)
	}
	list, _ = m.ListRoots(ctx, &rootsv1.ListRootsRequest{})
	if len(list.Roots) != 0 {
		t.Errorf("after delete: %d", len(list.Roots))
	}
}

func TestGetRoot(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := mustTempDir(t, t.TempDir(), "lib")

	created, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: dir})
	if err != nil {
		t.Fatal(err)
	}

	got, err := m.GetRoot(ctx, &rootsv1.GetRootRequest{Id: created.Root.Id})
	if err != nil {
		t.Fatal(err)
	}
	if got.Root.Id != created.Root.Id {
		t.Errorf("id: %q", got.Root.Id)
	}

	_, err = m.GetRoot(ctx, &rootsv1.GetRootRequest{Id: "missing"})
	expectCode(t, err, codes.NotFound)

	_, err = m.GetRoot(ctx, &rootsv1.GetRootRequest{})
	expectCode(t, err, codes.InvalidArgument)
}

func TestUpdateRootMerge(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	base := t.TempDir()
	dir := mustTempDir(t, base, "Movies")

	created, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{
		Path:             dir,
		Name:             "Movies",
		MediaKind:        "movies",
		NamingTemplateId: "tpl_a",
	})
	if err != nil {
		t.Fatal(err)
	}

	byName, err := m.UpdateRoot(ctx, &rootsv1.UpdateRootRequest{
		Id:   created.Root.Id,
		Name: "Feature Films",
	})
	if err != nil {
		t.Fatal(err)
	}
	if byName.Root.Path != dir || byName.Root.Name != "Feature Films" || byName.Root.NamingTemplateId != "tpl_a" {
		t.Errorf("name-only merge: %+v", byName.Root)
	}

	byTemplate, err := m.UpdateRoot(ctx, &rootsv1.UpdateRootRequest{
		Id:               created.Root.Id,
		NamingTemplateId: "tpl_b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if byTemplate.Root.Path != dir || byTemplate.Root.Name != "Feature Films" || byTemplate.Root.NamingTemplateId != "tpl_b" {
		t.Errorf("template-only merge: %+v", byTemplate.Root)
	}
}

func TestRejectRelativeDuplicateAndMissing(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	base := t.TempDir()
	media := mustTempDir(t, base, "media")

	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: "relative/path"}); err == nil {
		t.Fatal("expected relative path error")
	}
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: ""}); err == nil {
		t.Fatal("expected empty path error")
	}
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: filepath.Join(base, "missing")}); err == nil {
		t.Fatal("expected missing path error")
	}
	file := filepath.Join(base, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: file}); err == nil {
		t.Fatal("expected non-directory error")
	}

	_, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: media})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: media}); err == nil {
		t.Fatal("expected duplicate path error")
	}
	dup := filepath.Join(media, "..", filepath.Base(media))
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: dup}); err == nil {
		t.Fatal("expected duplicate after clean")
	}
}

func TestPathNormalization(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	movies := mustTempDir(t, t.TempDir(), "Movies")

	created, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{
		Path: movies + string(filepath.Separator),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Root.Path != movies {
		t.Errorf("cleaned path: %q", created.Root.Path)
	}
}

func TestMediaKindFilter(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	base := t.TempDir()
	movies := mustTempDir(t, base, "movies")
	tv := mustTempDir(t, base, "tv")
	music := mustTempDir(t, base, "music")
	books := mustTempDir(t, base, "books")
	shared := mustTempDir(t, base, "shared")

	_, _ = m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: movies, MediaKind: "movies"})
	_, _ = m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: tv, MediaKind: "tv"})
	_, _ = m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: music, MediaKind: "music"})
	_, _ = m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: books, MediaKind: "books"})
	_, _ = m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: shared, MediaKind: "any"})

	list, err := m.ListRoots(ctx, &rootsv1.ListRootsRequest{MediaKind: "movies"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Roots) != 2 {
		t.Fatalf("movies filter: %d", len(list.Roots))
	}

	musicList, err := m.ListRoots(ctx, &rootsv1.ListRootsRequest{MediaKind: "music"})
	if err != nil {
		t.Fatal(err)
	}
	if len(musicList.Roots) != 2 {
		t.Fatalf("music filter: %d", len(musicList.Roots))
	}

	booksList, err := m.ListRoots(ctx, &rootsv1.ListRootsRequest{MediaKind: "books"})
	if err != nil {
		t.Fatal(err)
	}
	if len(booksList.Roots) != 2 {
		t.Fatalf("books filter: %d", len(booksList.Roots))
	}
}

func TestSeedFromPaths(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	base := t.TempDir()
	a := mustTempDir(t, base, "a")
	b := mustTempDir(t, base, "b")

	first, err := m.SeedFromPaths(ctx, &rootsv1.SeedFromPathsRequest{
		Paths:     []string{a, b},
		MediaKind: "movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Created) != 2 || len(first.Existing) != 0 {
		t.Fatalf("first seed: created=%d existing=%d", len(first.Created), len(first.Existing))
	}

	c := mustTempDir(t, base, "c")
	second, err := m.SeedFromPaths(ctx, &rootsv1.SeedFromPathsRequest{
		Paths: []string{a, c},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Created) != 1 || len(second.Existing) != 1 {
		t.Fatalf("second seed: created=%d existing=%d", len(second.Created), len(second.Existing))
	}
	if second.Existing[0].Path != a {
		t.Errorf("existing path: %q", second.Existing[0].Path)
	}
}

func TestSeedFromPathsSkipValidation(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "not-created-yet")

	resp, err := m.SeedFromPaths(ctx, &rootsv1.SeedFromPathsRequest{
		Paths:           []string{missing},
		SkipValidation:  true,
		MediaKind:       "movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Created) != 1 {
		t.Fatalf("created: %d", len(resp.Created))
	}

	_, err = m.SeedFromPaths(ctx, &rootsv1.SeedFromPathsRequest{
		Paths: []string{missing},
	})
	expectCode(t, err, codes.InvalidArgument)
}

func TestInvalidMediaKind(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := mustTempDir(t, t.TempDir(), "x")
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: dir, MediaKind: "podcasts"}); err == nil {
		t.Fatal("expected invalid kind")
	}
	_, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: dir, MediaKind: "books"})
	if err != nil {
		t.Fatal(err)
	}
	comics := mustTempDir(t, t.TempDir(), "comics")
	if _, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: comics, MediaKind: "comics"}); err != nil {
		t.Fatal(err)
	}
}

func TestProbeRoot(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := mustTempDir(t, t.TempDir(), "lib")

	resp, err := m.ProbeRoot(ctx, &rootsv1.ProbeRootRequest{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Accessible || resp.FreeBytes <= 0 {
		t.Errorf("probe: %+v", resp)
	}

	missing, err := m.ProbeRoot(ctx, &rootsv1.ProbeRootRequest{Path: filepath.Join(dir, "nope")})
	if err != nil {
		t.Fatal(err)
	}
	if missing.Accessible || missing.Error == "" {
		t.Errorf("expected inaccessible: %+v", missing)
	}
}

func TestBrowsePath(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	base := t.TempDir()
	child := mustTempDir(t, base, "child")
	_ = mustTempDir(t, child, "nested")
	_ = mustTempDir(t, child, ".hidden")
	if err := os.WriteFile(filepath.Join(child, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	resp, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: child})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Path != child {
		t.Errorf("path: %q", resp.Path)
	}
	if resp.Parent != base {
		t.Errorf("parent: %q want %q", resp.Parent, base)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Name != "nested" {
		t.Fatalf("entries: %+v", resp.Entries)
	}

	if _, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: "relative"}); err == nil {
		t.Fatal("expected relative reject")
	}
}

func TestBrowsePathPrefixAllowlist(t *testing.T) {
	base := t.TempDir()
	allowed := mustTempDir(t, base, "media")
	outside := mustTempDir(t, base, "outside")

	m := NewModule(Config{
		DBPath:         filepath.Join(t.TempDir(), "roots.db"),
		GRPCAddr:       ":0",
		BrowsePrefixes: []string{allowed},
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	if _, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: allowed}); err != nil {
		t.Fatal(err)
	}
	_, err := m.BrowsePath(ctx, &rootsv1.BrowsePathRequest{Path: outside})
	expectCode(t, err, codes.InvalidArgument)
}

func TestDeleteRootErrors(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.DeleteRoot(ctx, &rootsv1.DeleteRootRequest{})
	expectCode(t, err, codes.InvalidArgument)

	_, err = m.DeleteRoot(ctx, &rootsv1.DeleteRootRequest{Id: "rf_missing"})
	expectCode(t, err, codes.NotFound)
}

func TestPickRootDefaultAndFallback(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	base := t.TempDir()
	small := mustTempDir(t, base, "small")
	large := mustTempDir(t, base, "large")

	smallRoot, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: small, MediaKind: "movies"})
	if err != nil {
		t.Fatal(err)
	}
	largeRoot, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{Path: large, MediaKind: "movies"})
	if err != nil {
		t.Fatal(err)
	}

	picked, err := m.PickRoot(ctx, &rootsv1.PickRootRequest{MediaKind: "movies"})
	if err != nil {
		t.Fatal(err)
	}
	if picked.Root.Id != largeRoot.Root.Id && picked.Root.Id != smallRoot.Root.Id {
		t.Fatalf("unexpected pick: %s", picked.Root.Id)
	}

	def := true
	_, err = m.UpdateRoot(ctx, &rootsv1.UpdateRootRequest{
		Id:        smallRoot.Root.Id,
		IsDefault: &def,
	})
	if err != nil {
		t.Fatal(err)
	}

	picked, err = m.PickRoot(ctx, &rootsv1.PickRootRequest{MediaKind: "movies"})
	if err != nil {
		t.Fatal(err)
	}
	if picked.Root.Id != smallRoot.Root.Id {
		t.Fatalf("default pick: got %s want %s", picked.Root.Id, smallRoot.Root.Id)
	}
}

func TestNamingTemplateID(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	dir := mustTempDir(t, t.TempDir(), "lib")

	created, err := m.CreateRoot(ctx, &rootsv1.CreateRootRequest{
		Path:             dir,
		NamingTemplateId: "tpl_movie_plex",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Root.NamingTemplateId != "tpl_movie_plex" {
		t.Errorf("template: %q", created.Root.NamingTemplateId)
	}
}
