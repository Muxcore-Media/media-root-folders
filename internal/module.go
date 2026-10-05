package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Muxcore-Media/core/sdk/go/module/meshtls"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	manifest "github.com/Muxcore-Media/media-root-folders"
	rootsv1 "github.com/Muxcore-Media/media-root-folders/proto/rootsv1"

	"github.com/Muxcore-Media/core/pkg/contracts"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	_ "modernc.org/sqlite"
)

const (
	mediaKindAny        = "any"
	mediaKindMovies     = "movies"
	mediaKindTV         = "tv"
	mediaKindMusic      = "music"
	mediaKindBooks      = "books"
	mediaKindAudiobooks = "audiobooks"
	mediaKindComics     = "comics"

	settingKeyRequireAccessible = "require_accessible"
)

type Module struct { //nolint:govet // fieldalignment: lifecycle fields grouped for readability
	rootsv1.UnimplementedRootFolderServiceServer

	mu    sync.RWMutex
	cfgMu sync.RWMutex
	db    *sql.DB

	id                string
	dbPath            string
	grpcAddr          string
	requireAccessible bool
	browsePrefixes    []string
	grpcSrv           *grpc.Server
	grpcLis           net.Listener
}

type Config struct {
	ID                string
	DBPath            string
	GRPCAddr          string
	RequireAccessible *bool
	BrowsePrefixes    []string
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "media-root-folders"
	}
	if cfg.DBPath == "" {
		cfg.DBPath = "/var/lib/media-root-folders/roots.db"
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = ":9540"
	}
	if v := os.Getenv("ROOTS_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("ROOTS_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	require := true
	if cfg.RequireAccessible != nil {
		require = *cfg.RequireAccessible
	}
	if v := os.Getenv("ROOTS_REQUIRE_ACCESSIBLE"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			require = b
		}
	}
	prefixes := cfg.BrowsePrefixes
	if len(prefixes) == 0 {
		if v := os.Getenv("ROOTS_BROWSE_PREFIXES"); v != "" {
			prefixes = parseBrowsePrefixes(v)
		} else {
			prefixes = []string{"/"}
		}
	}
	return &Module{
		id:                cfg.ID,
		dbPath:            cfg.DBPath,
		grpcAddr:          cfg.GRPCAddr,
		requireAccessible: require,
		browsePrefixes:    prefixes,
	}
}

func parseBrowsePrefixes(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if cleaned, err := normalizePath(p); err == nil {
			out = append(out, cleaned)
		}
	}
	if len(out) == 0 {
		return []string{"/"}
	}
	return out
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:             m.id,
		Name:           "Root Folders",
		Version:        modulesdk.ManifestVersion(manifest.ManifestJSON),
		Roles:          []string{"library-config"},
		Description:    "Managed library root-folder catalog for movies and TV path binding",
		Author:         "MuxCore",
		Capabilities:   []string{"media.roots", "settings"},
		MinCoreVersion: "0.4.0",
		HTTPAddr:       m.grpcAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	dir := filepath.Dir(m.dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		_ = db.Close()
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS root_folders (
			id         TEXT PRIMARY KEY,
			path       TEXT NOT NULL UNIQUE,
			name       TEXT NOT NULL,
			media_kind TEXT NOT NULL DEFAULT 'any',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create root_folders table: %w", err)
	}
	if _, err = db.ExecContext(ctx, `ALTER TABLE root_folders ADD COLUMN naming_template_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return fmt.Errorf("migrate naming_template_id: %w", err)
		}
	}
	if _, err = db.ExecContext(ctx, `ALTER TABLE root_folders ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			_ = db.Close()
			return fmt.Errorf("migrate is_default: %w", err)
		}
	}
	if _, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS settings_kv (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`); err != nil {
		_ = db.Close()
		return fmt.Errorf("create settings_kv table: %w", err)
	}

	m.mu.Lock()
	m.db = db
	m.mu.Unlock()

	if loadErr := m.loadPersistedSettings(ctx); loadErr != nil {
		_ = db.Close()
		return loadErr
	}

	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", m.grpcAddr)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.grpcLis = lis

	slog.Info("media-root-folders initialized", "db", m.dbPath, "grpc", m.grpcAddr)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	srv, err := meshtls.NewServer()
	if err != nil {
		return fmt.Errorf("mesh tls: %w", err)
	}
	m.grpcSrv = srv
	rootsv1.RegisterRootFolderServiceServer(m.grpcSrv, m)
	modulesdk.RegisterSettings(m.grpcSrv, m.id, m)

	go func() {
		slog.Info("media-root-folders gRPC started", "addr", m.grpcAddr)
		if err := m.grpcSrv.Serve(m.grpcLis); err != nil {
			slog.Error("media-root-folders gRPC error", "error", err)
		}
	}()
	return nil
}

func (m *Module) Stop(ctx context.Context) error {
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	m.mu.Lock()
	if m.db != nil {
		_ = m.db.Close()
		m.db = nil
	}
	m.mu.Unlock()
	slog.Info("media-root-folders stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
	m.mu.RLock()
	db := m.db
	m.mu.RUnlock()
	if db == nil {
		return fmt.Errorf("not initialized")
	}
	return db.PingContext(ctx)
}

func invalidArg(format string, args ...any) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

func notFound(format string, args ...any) error {
	return status.Errorf(codes.NotFound, format, args...)
}

func internalErr(err error) error {
	return status.Errorf(codes.Internal, "%v", err)
}

func normalizePath(path string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path must be absolute")
	}
	return cleaned, nil
}

func normalizeMediaKind(kind string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		return mediaKindAny, nil
	}
	switch k {
	case mediaKindAny, mediaKindMovies, mediaKindTV, mediaKindMusic, mediaKindBooks, mediaKindAudiobooks, mediaKindComics:
		return k, nil
	default:
		return "", fmt.Errorf("media_kind must be movies, tv, music, books, audiobooks, comics, or any")
	}
}

func defaultName(path, name string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	base := filepath.Base(path)
	if base == "" || base == string(filepath.Separator) {
		return path
	}
	return base
}

func requireAccessibleDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("path not accessible: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path is not a directory")
	}
	f, err := os.Open(path) //nolint:gosec // path validated as absolute and operator-controlled root folder
	if err != nil {
		return fmt.Errorf("path not readable: %w", err)
	}
	_ = f.Close()
	return nil
}

func probePath(path string) (accessible bool, freeBytes, totalBytes int64, errMsg string) {
	if err := requireAccessibleDir(path); err != nil {
		return false, 0, 0, err.Error()
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return true, 0, 0, ""
	}
	if stat.Bsize <= 0 {
		return true, 0, 0, ""
	}
	bsize := uint64(stat.Bsize)
	return true, int64(stat.Bavail * bsize), int64(stat.Blocks * bsize), "" //nolint:gosec // disk stats fit int64 for homelab volumes
}

// cloneRoot deep-copies a root folder. proto.Clone preserves the concrete
// type, so the fallback below is unreachable and only satisfies the checked
// assertion.
func cloneRoot(r *rootsv1.RootFolder) *rootsv1.RootFolder {
	c, ok := proto.Clone(r).(*rootsv1.RootFolder)
	if !ok {
		return r
	}
	return c
}

func enrichRoot(r *rootsv1.RootFolder) {
	if r == nil {
		return
	}
	ok, free, total, _ := probePath(r.Path)
	r.Accessible = ok
	r.FreeBytes = free
	r.TotalBytes = total
}

func pathUnderPrefix(path, prefix string) bool {
	path = filepath.Clean(path)
	prefix = filepath.Clean(prefix)
	if prefix == string(filepath.Separator) {
		return filepath.IsAbs(path)
	}
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+string(filepath.Separator))
}

func (m *Module) pathAllowedByBrowsePrefixes(path string) bool {
	for _, prefix := range m.browsePrefixes {
		if pathUnderPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (m *Module) ListRoots(ctx context.Context, req *rootsv1.ListRootsRequest) (*rootsv1.ListRootsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	kind := strings.ToLower(strings.TrimSpace(req.GetMediaKind()))
	roots, err := m.loadRootsLocked(ctx, kind)
	if err != nil {
		return nil, internalErr(err)
	}
	for _, r := range roots {
		enrichRoot(r)
	}
	return &rootsv1.ListRootsResponse{Roots: roots}, nil
}

func (m *Module) GetRoot(ctx context.Context, req *rootsv1.GetRootRequest) (*rootsv1.GetRootResponse, error) {
	if strings.TrimSpace(req.GetId()) == "" {
		return nil, invalidArg("id is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	root, err := m.loadRootLocked(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("root not found: %s", req.GetId())
		}
		return nil, internalErr(err)
	}
	enrichRoot(root)
	return &rootsv1.GetRootResponse{Root: root}, nil
}

func (m *Module) CreateRoot(ctx context.Context, req *rootsv1.CreateRootRequest) (*rootsv1.CreateRootResponse, error) {
	path, err := normalizePath(req.GetPath())
	if err != nil {
		return nil, invalidArg("%v", err)
	}
	if m.getRequireAccessible() {
		if dirErr := requireAccessibleDir(path); dirErr != nil {
			return nil, invalidArg("%v", dirErr)
		}
	}
	kind, err := normalizeMediaKind(req.GetMediaKind())
	if err != nil {
		return nil, invalidArg("%v", err)
	}
	name := defaultName(path, req.GetName())
	tplID := strings.TrimSpace(req.GetNamingTemplateId())
	isDefault := req.GetIsDefault()

	m.mu.Lock()
	defer m.mu.Unlock()

	if isDefault {
		if clearErr := m.clearDefaultForKindLocked(ctx, kind); clearErr != nil {
			return nil, internalErr(clearErr)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("rf_%d", time.Now().UnixNano())
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO root_folders (id, path, name, media_kind, naming_template_id, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, path, name, kind, tplID, boolToInt(isDefault), now, now,
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, invalidArg("root path already exists: %s", path)
		}
		return nil, internalErr(fmt.Errorf("create root: %w", err))
	}
	root, err := m.loadRootLocked(ctx, id)
	if err != nil {
		return nil, internalErr(err)
	}
	enrichRoot(root)
	return &rootsv1.CreateRootResponse{Root: root}, nil
}

func (m *Module) UpdateRoot(ctx context.Context, req *rootsv1.UpdateRootRequest) (*rootsv1.UpdateRootResponse, error) {
	if strings.TrimSpace(req.GetId()) == "" {
		return nil, invalidArg("id is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := m.loadRootLocked(ctx, req.GetId())
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound("root not found: %s", req.GetId())
		}
		return nil, internalErr(err)
	}

	path := existing.Path
	if strings.TrimSpace(req.GetPath()) != "" {
		path, err = normalizePath(req.GetPath())
		if err != nil {
			return nil, invalidArg("%v", err)
		}
	}
	if m.getRequireAccessible() && strings.TrimSpace(req.GetPath()) != "" {
		if dirErr := requireAccessibleDir(path); dirErr != nil {
			return nil, invalidArg("%v", dirErr)
		}
	}

	kind := existing.MediaKind
	if strings.TrimSpace(req.GetMediaKind()) != "" {
		kind, err = normalizeMediaKind(req.GetMediaKind())
		if err != nil {
			return nil, invalidArg("%v", err)
		}
	}

	name := existing.Name
	if strings.TrimSpace(req.GetName()) != "" {
		name = defaultName(path, req.GetName())
	}

	tplID := existing.NamingTemplateId
	if strings.TrimSpace(req.GetNamingTemplateId()) != "" {
		tplID = strings.TrimSpace(req.GetNamingTemplateId())
	}

	isDefault := existing.IsDefault
	if req.IsDefault != nil {
		isDefault = req.GetIsDefault()
		if isDefault {
			if clearErr := m.clearDefaultForKindLocked(ctx, kind); clearErr != nil {
				return nil, internalErr(clearErr)
			}
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := m.db.ExecContext(ctx,
		`UPDATE root_folders SET path=?, name=?, media_kind=?, naming_template_id=?, is_default=?, updated_at=? WHERE id=?`,
		path, name, kind, tplID, boolToInt(isDefault), now, req.GetId(),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return nil, invalidArg("root path already exists: %s", path)
		}
		return nil, internalErr(fmt.Errorf("update root: %w", err))
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, notFound("root not found: %s", req.GetId())
	}
	root, err := m.loadRootLocked(ctx, req.GetId())
	if err != nil {
		return nil, internalErr(err)
	}
	enrichRoot(root)
	return &rootsv1.UpdateRootResponse{Root: root}, nil
}

func (m *Module) DeleteRoot(ctx context.Context, req *rootsv1.DeleteRootRequest) (*rootsv1.DeleteRootResponse, error) {
	if strings.TrimSpace(req.GetId()) == "" {
		return nil, invalidArg("id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.db.ExecContext(ctx, `DELETE FROM root_folders WHERE id = ?`, req.GetId())
	if err != nil {
		return nil, internalErr(err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, notFound("root not found: %s", req.GetId())
	}
	return &rootsv1.DeleteRootResponse{}, nil
}

func (m *Module) SeedFromPaths(ctx context.Context, req *rootsv1.SeedFromPathsRequest) (*rootsv1.SeedFromPathsResponse, error) {
	kind, err := normalizeMediaKind(req.GetMediaKind())
	if err != nil {
		return nil, invalidArg("%v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var created, existing []*rootsv1.RootFolder
	for _, raw := range req.GetPaths() {
		path, err := normalizePath(raw)
		if err != nil {
			return nil, invalidArg("%v", err)
		}
		if !req.GetSkipValidation() && m.getRequireAccessible() {
			if dirErr := requireAccessibleDir(path); dirErr != nil {
				return nil, invalidArg("%v", dirErr)
			}
		}
		if found := m.loadRootByPathLocked(ctx, path); found != nil {
			enrichRoot(found)
			existing = append(existing, found)
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339)
		id := fmt.Sprintf("rf_%d", time.Now().UnixNano())
		name := defaultName(path, "")
		_, err = m.db.ExecContext(ctx,
			`INSERT INTO root_folders (id, path, name, media_kind, naming_template_id, is_default, created_at, updated_at) VALUES (?, ?, ?, ?, '', 0, ?, ?)`,
			id, path, name, kind, now, now,
		)
		if err != nil {
			if isUniqueConstraint(err) {
				if found := m.loadRootByPathLocked(ctx, path); found != nil {
					enrichRoot(found)
					existing = append(existing, found)
					continue
				}
			}
			return nil, internalErr(fmt.Errorf("seed path %s: %w", path, err))
		}
		root, err := m.loadRootLocked(ctx, id)
		if err != nil {
			return nil, internalErr(err)
		}
		enrichRoot(root)
		created = append(created, root)
	}
	return &rootsv1.SeedFromPathsResponse{Created: created, Existing: existing}, nil
}

func (m *Module) ProbeRoot(ctx context.Context, req *rootsv1.ProbeRootRequest) (*rootsv1.ProbeRootResponse, error) {
	path, err := normalizePath(req.GetPath())
	if err != nil {
		return nil, invalidArg("%v", err)
	}
	ok, free, total, errMsg := probePath(path)
	return &rootsv1.ProbeRootResponse{
		Path:       path,
		Accessible: ok,
		FreeBytes:  free,
		TotalBytes: total,
		Error:      errMsg,
	}, nil
}

func (m *Module) BrowsePath(ctx context.Context, req *rootsv1.BrowsePathRequest) (*rootsv1.BrowsePathResponse, error) {
	raw := strings.TrimSpace(req.GetPath())
	if raw == "" {
		raw = "/"
	}
	path, err := normalizePath(raw)
	if err != nil {
		return nil, invalidArg("%v", err)
	}
	if !m.pathAllowedByBrowsePrefixes(path) {
		return nil, invalidArg("path outside allowed browse prefixes")
	}
	if dirErr := requireAccessibleDir(path); dirErr != nil {
		return nil, invalidArg("%v", dirErr)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, internalErr(fmt.Errorf("read directory: %w", err))
	}

	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}

	out := make([]*rootsv1.BrowseEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		child := filepath.Join(path, e.Name())
		cleaned := filepath.Clean(child)
		if !pathUnderPrefix(cleaned, path) && path != string(filepath.Separator) {
			continue
		}
		if !m.pathAllowedByBrowsePrefixes(cleaned) {
			continue
		}
		out = append(out, &rootsv1.BrowseEntry{
			Name:  e.Name(),
			Path:  cleaned,
			IsDir: true,
		})
	}
	return &rootsv1.BrowsePathResponse{
		Path:    path,
		Parent:  parent,
		Entries: out,
	}, nil
}

func (m *Module) PickRoot(ctx context.Context, req *rootsv1.PickRootRequest) (*rootsv1.PickRootResponse, error) {
	kind, err := normalizeMediaKind(req.GetMediaKind())
	if err != nil {
		return nil, invalidArg("%v", err)
	}
	if kind == mediaKindAny {
		return nil, invalidArg("media_kind is required for PickRoot")
	}

	m.mu.RLock()
	roots, err := m.loadRootsLocked(ctx, kind)
	m.mu.RUnlock()
	if err != nil {
		return nil, internalErr(err)
	}

	var picked *rootsv1.RootFolder
	var anyDefault *rootsv1.RootFolder
	for _, r := range roots {
		enrichRoot(r)
		if !r.Accessible {
			continue
		}
		if r.IsDefault {
			if r.MediaKind == kind {
				return &rootsv1.PickRootResponse{Root: r}, nil
			}
			if r.MediaKind == mediaKindAny && anyDefault == nil {
				anyDefault = cloneRoot(r)
			}
		}
		if picked == nil || r.FreeBytes > picked.FreeBytes {
			picked = cloneRoot(r)
		}
	}
	if anyDefault != nil {
		return &rootsv1.PickRootResponse{Root: anyDefault}, nil
	}
	if picked == nil {
		return nil, notFound("no accessible root for kind %s", kind)
	}
	return &rootsv1.PickRootResponse{Root: picked}, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (m *Module) clearDefaultForKindLocked(ctx context.Context, kind string) error {
	_, err := m.db.ExecContext(ctx, `UPDATE root_folders SET is_default = 0 WHERE media_kind = ?`, kind)
	return err
}

func (m *Module) loadRootsLocked(ctx context.Context, mediaKind string) ([]*rootsv1.RootFolder, error) {
	var rows *sql.Rows
	var err error
	if mediaKind == "" {
		rows, err = m.db.QueryContext(ctx, `SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), is_default, created_at, updated_at FROM root_folders ORDER BY path`)
	} else {
		rows, err = m.db.QueryContext(ctx,
			`SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), is_default, created_at, updated_at FROM root_folders
			 WHERE media_kind = ? OR media_kind = 'any' ORDER BY path`,
			mediaKind,
		)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*rootsv1.RootFolder
	for rows.Next() {
		var r rootsv1.RootFolder
		var isDef int
		if err := rows.Scan(&r.Id, &r.Path, &r.Name, &r.MediaKind, &r.NamingTemplateId, &isDef, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.IsDefault = isDef != 0
		out = append(out, &r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (m *Module) loadRootLocked(ctx context.Context, id string) (*rootsv1.RootFolder, error) {
	var r rootsv1.RootFolder
	var isDef int
	err := m.db.QueryRowContext(ctx,
		`SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), is_default, created_at, updated_at FROM root_folders WHERE id = ?`, id,
	).Scan(&r.Id, &r.Path, &r.Name, &r.MediaKind, &r.NamingTemplateId, &isDef, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.IsDefault = isDef != 0
	return &r, nil
}

func (m *Module) loadRootByPathLocked(ctx context.Context, path string) *rootsv1.RootFolder {
	var r rootsv1.RootFolder
	var isDef int
	err := m.db.QueryRowContext(ctx,
		`SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), is_default, created_at, updated_at FROM root_folders WHERE path = ?`, path,
	).Scan(&r.Id, &r.Path, &r.Name, &r.MediaKind, &r.NamingTemplateId, &isDef, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil
	}
	r.IsDefault = isDef != 0
	return &r
}
