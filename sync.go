package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Syncer struct {
	cfg      *Config
	outline  *Outline
	reposDir string

	collections map[string]string // name -> id, refreshed every cycle
	canDelete   bool              // false once Outline refuses a delete (non-admin token)
}

func NewSyncer(cfg *Config, reposDir string) *Syncer {
	return &Syncer{
		cfg:       cfg,
		outline:   NewOutline(cfg.OutlineURL, cfg.OutlineToken),
		reposDir:  reposDir,
		canDelete: cfg.CleanupEnabled() || cfg.PurgeOldExports,
	}
}

// RunCycle syncs every configured task once.
func (s *Syncer) RunCycle(ctx context.Context) {
	if err := s.loadCollections(ctx); err != nil {
		log.Printf("ERROR fetching collections: %v", err)
		return
	}
	if s.cfg.PurgeOldExports {
		s.purgeOldExports(ctx)
	}
	for _, t := range s.cfg.SyncTasks {
		if ctx.Err() != nil {
			return
		}
		if err := s.syncTask(ctx, t); err != nil {
			log.Printf("[%s] ERROR: %v", t.CollectionName, err)
		}
	}
}

func (s *Syncer) loadCollections(ctx context.Context) error {
	cols, err := s.outline.Collections(ctx)
	if err != nil {
		return err
	}
	s.collections = make(map[string]string, len(cols))
	for _, c := range cols {
		s.collections[c.Name] = c.ID
	}
	return nil
}

func (s *Syncer) collectionID(name string) (string, error) {
	if id, ok := s.collections[name]; ok {
		return id, nil
	}
	for n, id := range s.collections {
		if strings.EqualFold(n, name) {
			log.Printf("[%s] matched collection %q case-insensitively", name, n)
			return id, nil
		}
	}
	return "", fmt.Errorf("collection %q not found (check the name and token permissions)", name)
}

func (s *Syncer) syncTask(ctx context.Context, t Task) error {
	name := t.CollectionName
	log.Printf("[%s] processing", name)

	colID, err := s.collectionID(name)
	if err != nil {
		return err
	}

	// Same folder layout as the Python version so existing volumes keep working.
	repo := &Repo{Dir: filepath.Join(s.reposDir, strings.ReplaceAll(name, " ", "_"))}
	if err := repo.Init(t.RepoURL, t.Branch, s.cfg.GitUser, s.cfg.GitEmail); err != nil {
		return fmt.Errorf("prepare repo: %w", err)
	}

	zipPath, err := s.export(ctx, name, colID)
	if err != nil {
		return err
	}
	defer os.Remove(zipPath)

	// Bring the local branch in line with the remote before committing, so a
	// lost volume, an earlier failed push or a commit made directly on GitHub
	// never leaves us unable to push. Outline stays the source of truth.
	remoteExists, err := repo.FetchRemote(t.Branch, s.cfg.PurgeLocalHistory)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	if err := ClearWorkTree(repo.Dir); err != nil {
		return fmt.Errorf("clean work tree: %w", err)
	}
	if err := Extract(zipPath, repo.Dir); err != nil {
		return err
	}

	colDir := FindCollectionDir(repo.Dir, name)
	readme := filepath.Join(repo.Dir, "README.md")
	if s.cfg.NavigationEnabled() {
		if err := AddNavigation(colDir, readme, name); err != nil {
			log.Printf("[%s] WARN %v", name, err)
		}
	}
	if err := WriteReadme(readme, colDir, name, time.Now()); err != nil {
		log.Printf("[%s] WARN README: %v", name, err)
	}

	remoteRef := "refs/remotes/origin/" + t.Branch
	if remoteExists {
		if _, err := repo.Git("reset", "--quiet", "--mixed", remoteRef); err != nil {
			return err
		}
	}
	if _, err := repo.Git("add", "-A"); err != nil {
		return err
	}

	_, hasHead := repo.RevParse("HEAD")
	changed, err := repo.HasChanges()
	if err != nil {
		return err
	}
	committed := false
	if !hasHead || changed {
		msg := "Outline Sync: " + time.Now().Format("2006-01-02 15:04:05")
		if _, err := repo.Git("commit", "--quiet", "-m", msg); err != nil {
			return err
		}
		committed = true
		log.Printf("[%s] changes committed", name)
	} else {
		log.Printf("[%s] no changes detected", name)
	}

	head, _ := repo.RevParse("HEAD")
	remote, _ := repo.RevParse(remoteRef)
	if head != remote {
		log.Printf("[%s] pushing to %s", name, t.Branch)
		if _, err := repo.Git("push", "--quiet", "origin", "HEAD:refs/heads/"+t.Branch); err != nil {
			return fmt.Errorf("push: %w", err)
		}
		log.Printf("[%s] pushed", name)
	}

	if committed && s.cfg.PurgeLocalHistory {
		if err := repo.Compact(); err != nil {
			log.Printf("[%s] WARN compacting repo: %v", name, err)
		}
	}
	return nil
}

// export builds and downloads a collection export, then removes it from
// Outline so exports do not pile up in its storage.
func (s *Syncer) export(ctx context.Context, name, colID string) (string, error) {
	opID, err := s.outline.StartExport(ctx, colID)
	if err != nil {
		return "", fmt.Errorf("start export: %w", err)
	}
	if s.cfg.CleanupEnabled() {
		// Runs even if waiting or downloading fails, so failed exports are
		// cleaned up too. Uses a fresh context so shutdown does not skip it.
		defer func() {
			dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			s.deleteExport(dctx, name, opID)
		}()
	}
	if err := s.outline.WaitForExport(ctx, opID, s.cfg.ExportWait()); err != nil {
		return "", err
	}
	path, err := s.outline.DownloadExport(ctx, opID)
	if err != nil {
		return "", fmt.Errorf("download export: %w", err)
	}
	return path, nil
}

func (s *Syncer) deleteExport(ctx context.Context, name, opID string) {
	if !s.canDelete {
		return
	}
	err := s.outline.DeleteFileOperation(ctx, opID)
	switch {
	case err == nil:
	case IsForbidden(err):
		s.canDelete = false
		log.Printf("WARN Outline refused to delete export %s (%v). Deleting exports needs an admin API token; "+
			"exports will keep accumulating until you use one or delete them under Settings → Export.", opID, err)
	default:
		log.Printf("[%s] WARN could not delete export %s: %v", name, opID, err)
	}
}

// purgeOldExports deletes exports left behind by earlier runs: markdown
// exports of the synced collections created by the token's own user.
func (s *Syncer) purgeOldExports(ctx context.Context) {
	if !s.canDelete {
		return
	}
	me, err := s.outline.CurrentUserID(ctx)
	if err != nil {
		log.Printf("WARN purge_old_exports: %v", err)
		return
	}
	synced := map[string]bool{}
	for _, t := range s.cfg.SyncTasks {
		if id, err := s.collectionID(t.CollectionName); err == nil {
			synced[id] = true
		}
	}
	ops, err := s.outline.Exports(ctx)
	if err != nil {
		if IsForbidden(err) {
			s.canDelete = false
		}
		log.Printf("WARN purge_old_exports: listing exports: %v", err)
		return
	}
	deleted := 0
	for _, op := range ops {
		if op.User.ID != me || !synced[op.CollectionID] || op.Format != exportFormat {
			continue
		}
		if op.State != "complete" && op.State != "error" && op.State != "expired" {
			continue // still being built (possibly by a sync running right now)
		}
		if err := s.outline.DeleteFileOperation(ctx, op.ID); err != nil {
			if IsForbidden(err) {
				s.canDelete = false
				log.Printf("WARN purge_old_exports: %v (admin token required)", err)
				return
			}
			if !errors.Is(err, context.Canceled) {
				log.Printf("WARN purge_old_exports: deleting %s: %v", op.ID, err)
			}
			continue
		}
		deleted++
	}
	if deleted > 0 {
		log.Printf("purge_old_exports: deleted %d old export(s) from Outline", deleted)
	}
}
