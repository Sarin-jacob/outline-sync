package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config mirrors the original config.yml schema. Every key that existed in the
// Python version keeps its name and meaning; new keys are optional and default
// to behaviour that is safe for existing setups.
type Config struct {
	OutlineURL        string  `yaml:"outline_url"`
	OutlineToken      string  `yaml:"outline_token"`
	CheckInterval     float64 `yaml:"check_interval"`
	GitUser           string  `yaml:"git_user"`
	GitEmail          string  `yaml:"git_email"`
	PurgeLocalHistory bool    `yaml:"purge_local_history"`

	// New in the Go version.
	CleanupExports  *bool   `yaml:"cleanup_exports"`   // delete each export from Outline after download (default true)
	PurgeOldExports bool    `yaml:"purge_old_exports"` // also delete leftover exports from earlier runs (default false)
	Navigation      *bool   `yaml:"navigation"`        // breadcrumbs, parent links and sub-page lists (default true)
	ExportTimeout   float64 `yaml:"export_timeout"`    // seconds to wait for Outline to build an export (default 1800)

	SyncTasks []Task `yaml:"sync_tasks"`
}

type Task struct {
	CollectionName string `yaml:"collection_name"`
	RepoURL        string `yaml:"repo_url"`
	Branch         string `yaml:"branch"`
}

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// Environment variables override the file, so secrets can live outside it.
	if v := os.Getenv("OUTLINE_URL"); v != "" {
		c.OutlineURL = v
	}
	if v := os.Getenv("OUTLINE_TOKEN"); v != "" {
		c.OutlineToken = v
	}

	c.OutlineURL = strings.TrimRight(c.OutlineURL, "/")
	if c.CheckInterval <= 0 {
		c.CheckInterval = 3600
	}
	if c.ExportTimeout <= 0 {
		c.ExportTimeout = 1800
	}
	if c.GitUser == "" {
		c.GitUser = "Outline Sync Bot"
	}
	if c.GitEmail == "" {
		c.GitEmail = "sync@jell0.online"
	}
	for i := range c.SyncTasks {
		if c.SyncTasks[i].Branch == "" {
			c.SyncTasks[i].Branch = "main"
		}
	}

	var errs []error
	if c.OutlineURL == "" {
		errs = append(errs, errors.New("outline_url is required"))
	}
	if c.OutlineToken == "" {
		errs = append(errs, errors.New("outline_token is required"))
	}
	if len(c.SyncTasks) == 0 {
		errs = append(errs, errors.New("sync_tasks is empty"))
	}
	for i, t := range c.SyncTasks {
		if t.CollectionName == "" {
			errs = append(errs, fmt.Errorf("sync_tasks[%d]: collection_name is required", i))
		}
		if t.RepoURL == "" {
			errs = append(errs, fmt.Errorf("sync_tasks[%d]: repo_url is required", i))
		}
	}
	return &c, errors.Join(errs...)
}

func (c *Config) Interval() time.Duration {
	return time.Duration(c.CheckInterval * float64(time.Second))
}

func (c *Config) ExportWait() time.Duration {
	return time.Duration(c.ExportTimeout * float64(time.Second))
}

func (c *Config) CleanupEnabled() bool    { return c.CleanupExports == nil || *c.CleanupExports }
func (c *Config) NavigationEnabled() bool { return c.Navigation == nil || *c.Navigation }
