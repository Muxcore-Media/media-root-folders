package internal

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/grpc"

	rootsv1 "github.com/Muxcore-Media/media-root-folders/proto/rootsv1"

	"github.com/Muxcore-Media/core/pkg/contracts"
	_ "modernc.org/sqlite"
)

const (
	mediaKindAny    = "any"
	mediaKindMovies = "movies"
	mediaKindTV     = "tv"
)

type Module struct {
	rootsv1.UnimplementedRootFolderServiceServer

	mu sync.RWMutex
	db *sql.DB

	id       string
	dbPath   string
	grpcAddr string
	grpcSrv  *grpc.Server
	grpcLis  net.Listener
}

type Config struct {
	ID       string
	DBPath   string
	GRPCAddr string
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
	return &Module{
		id:       cfg.ID,
		dbPath:   cfg.DBPath,
		grpcAddr: cfg.GRPCAddr,
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:             m.id,
		Name:           "Root Folders",
		Version:        "0.1.0",
		Roles:          []string{"library-config"},
		Description:    "Managed library root-folder catalog for movies and TV path binding",
		Author:         "MuxCore",
		Capabilities:   []string{"media.roots"},
		MinCoreVersion: "0.4.0",
		HTTPAddr:       m.grpcAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	dir := filepath.Dir(m.dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create db directory: %w", err)
	}

	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)

	if _, err := db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return fmt.Errorf("enable WAL: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS root_folders (
			id         TEXT PRIMARY KEY,
			path       TEXT NOT NULL UNIQUE,
			name       TEXT NOT NULL,
			media_kind TEXT NOT NULL DEFAULT 'any',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)
	`); err != nil {
		db.Close()
		return fmt.Errorf("create root_folders table: %w", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE root_folders ADD COLUMN naming_template_id TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return fmt.Errorf("migrate naming_template_id: %w", err)
		}
	}

	m.mu.Lock()
	m.db = db
	m.mu.Unlock()

	lis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		db.Close()
		return fmt.Errorf("listen gRPC %s: %w", m.grpcAddr, err)
	}
	m.grpcLis = lis

	slog.Info("media-root-folders initialized", "db", m.dbPath, "grpc", m.grpcAddr)
	return nil
}

func (m *Module) Start(ctx context.Context) error {
	m.grpcSrv = grpc.NewServer()
	rootsv1.RegisterRootFolderServiceServer(m.grpcSrv, m)

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
		m.db.Close()
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
	case mediaKindAny, mediaKindMovies, mediaKindTV:
		return k, nil
	default:
		return "", fmt.Errorf("media_kind must be movies, tv, or any")
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
	f, err := os.Open(path)
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
	return true, int64(stat.Bavail * bsize), int64(stat.Blocks * bsize), ""
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

func (m *Module) ListRoots(ctx context.Context, req *rootsv1.ListRootsRequest) (*rootsv1.ListRootsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	kind := strings.ToLower(strings.TrimSpace(req.GetMediaKind()))
	roots := m.loadRootsLocked(kind)
	for _, r := range roots {
		enrichRoot(r)
	}
	return &rootsv1.ListRootsResponse{Roots: roots}, nil
}

func (m *Module) CreateRoot(ctx context.Context, req *rootsv1.CreateRootRequest) (*rootsv1.CreateRootResponse, error) {
	path, err := normalizePath(req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := requireAccessibleDir(path); err != nil {
		return nil, err
	}
	kind, err := normalizeMediaKind(req.GetMediaKind())
	if err != nil {
		return nil, err
	}
	name := defaultName(path, req.GetName())
	tplID := strings.TrimSpace(req.GetNamingTemplateId())

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	id := fmt.Sprintf("rf_%d", time.Now().UnixNano())
	_, err = m.db.ExecContext(ctx,
		`INSERT INTO root_folders (id, path, name, media_kind, naming_template_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, path, name, kind, tplID, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create root: %w", err)
	}
	root := m.loadRootLocked(id)
	enrichRoot(root)
	return &rootsv1.CreateRootResponse{Root: root}, nil
}

func (m *Module) UpdateRoot(ctx context.Context, req *rootsv1.UpdateRootRequest) (*rootsv1.UpdateRootResponse, error) {
	if req.GetId() == "" {
		return nil, fmt.Errorf("id is required")
	}
	path, err := normalizePath(req.GetPath())
	if err != nil {
		return nil, err
	}
	if err := requireAccessibleDir(path); err != nil {
		return nil, err
	}
	kind, err := normalizeMediaKind(req.GetMediaKind())
	if err != nil {
		return nil, err
	}
	name := defaultName(path, req.GetName())
	tplID := strings.TrimSpace(req.GetNamingTemplateId())

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := m.db.ExecContext(ctx,
		`UPDATE root_folders SET path=?, name=?, media_kind=?, naming_template_id=?, updated_at=? WHERE id=?`,
		path, name, kind, tplID, now, req.GetId(),
	)
	if err != nil {
		return nil, fmt.Errorf("update root: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, fmt.Errorf("root not found: %s", req.GetId())
	}
	root := m.loadRootLocked(req.GetId())
	enrichRoot(root)
	return &rootsv1.UpdateRootResponse{Root: root}, nil
}

func (m *Module) DeleteRoot(ctx context.Context, req *rootsv1.DeleteRootRequest) (*rootsv1.DeleteRootResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.db.ExecContext(ctx, `DELETE FROM root_folders WHERE id = ?`, req.GetId())
	return &rootsv1.DeleteRootResponse{}, err
}

func (m *Module) SeedFromPaths(ctx context.Context, req *rootsv1.SeedFromPathsRequest) (*rootsv1.SeedFromPathsResponse, error) {
	kind, err := normalizeMediaKind(req.GetMediaKind())
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var created, existing []*rootsv1.RootFolder
	for _, raw := range req.GetPaths() {
		path, err := normalizePath(raw)
		if err != nil {
			return nil, err
		}
		if found := m.loadRootByPathLocked(path); found != nil {
			enrichRoot(found)
			existing = append(existing, found)
			continue
		}
		now := time.Now().UTC().Format(time.RFC3339)
		id := fmt.Sprintf("rf_%d", time.Now().UnixNano())
		name := defaultName(path, "")
		_, err = m.db.ExecContext(ctx,
			`INSERT INTO root_folders (id, path, name, media_kind, naming_template_id, created_at, updated_at) VALUES (?, ?, ?, ?, '', ?, ?)`,
			id, path, name, kind, now, now,
		)
		if err != nil {
			return nil, fmt.Errorf("seed path %s: %w", path, err)
		}
		root := m.loadRootLocked(id)
		enrichRoot(root)
		created = append(created, root)
	}
	return &rootsv1.SeedFromPathsResponse{Created: created, Existing: existing}, nil
}

func (m *Module) ProbeRoot(ctx context.Context, req *rootsv1.ProbeRootRequest) (*rootsv1.ProbeRootResponse, error) {
	path, err := normalizePath(req.GetPath())
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if err := requireAccessibleDir(path); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read directory: %w", err)
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
		child := filepath.Join(path, e.Name())
		cleaned := filepath.Clean(child)
		if !strings.HasPrefix(cleaned, path) && path != "/" {
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

func (m *Module) loadRootsLocked(mediaKind string) []*rootsv1.RootFolder {
	var rows *sql.Rows
	var err error
	if mediaKind == "" {
		rows, err = m.db.Query(`SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), created_at, updated_at FROM root_folders ORDER BY path`)
	} else {
		rows, err = m.db.Query(
			`SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), created_at, updated_at FROM root_folders
			 WHERE media_kind = ? OR media_kind = 'any' ORDER BY path`,
			mediaKind,
		)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []*rootsv1.RootFolder
	for rows.Next() {
		var r rootsv1.RootFolder
		if err := rows.Scan(&r.Id, &r.Path, &r.Name, &r.MediaKind, &r.NamingTemplateId, &r.CreatedAt, &r.UpdatedAt); err != nil {
			continue
		}
		out = append(out, &r)
	}
	return out
}

func (m *Module) loadRootLocked(id string) *rootsv1.RootFolder {
	var r rootsv1.RootFolder
	err := m.db.QueryRow(
		`SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), created_at, updated_at FROM root_folders WHERE id = ?`, id,
	).Scan(&r.Id, &r.Path, &r.Name, &r.MediaKind, &r.NamingTemplateId, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil
	}
	return &r
}

func (m *Module) loadRootByPathLocked(path string) *rootsv1.RootFolder {
	var r rootsv1.RootFolder
	err := m.db.QueryRow(
		`SELECT id, path, name, media_kind, COALESCE(naming_template_id, ''), created_at, updated_at FROM root_folders WHERE path = ?`, path,
	).Scan(&r.Id, &r.Path, &r.Name, &r.MediaKind, &r.NamingTemplateId, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil
	}
	return &r
}
