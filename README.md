#  Outline Git Sync

Automatically exports your self-hosted **Outline** collections as Markdown and syncs them to **Git** repositories.

##  Features

* **Smart Sync:** Commits and pushes only when actual changes are detected in your notes. Documents deleted in Outline are deleted in git too.
* **Easy to browse on GitHub:** Every document gets a breadcrumb trail at the top (`🏠 Collection › Parent › Page`), a list of its sub-pages and a **⬆ Back to parent** link at the bottom. The generated `README.md` shows the full document tree.
* **No export pile-up:** Each export is deleted from Outline once it has been downloaded, so Outline's storage stops filling up with zip files.
* **Self-healing git:** The repo is synced with the remote before every push, so a lost volume, a failed push or a commit made on GitHub never leaves the sync stuck.
* **Storage Optimized:** Optional `purge_local_history` keeps the container's `.git` folders tiny (shallow fetch + `git gc`).
* **Small & fast:** A single static Go binary on Alpine. Shuts down cleanly on `docker stop`.

---

##  Configuration (`config.yml`)

Create a `config.yml` in the root directory. You can add as many sync tasks as you need.

```yaml
outline_url: "https://docs.yourdomain.com"
outline_token: "outline_api_your_token_here"
check_interval: 3600       # Check for changes every hour (in seconds)
git_user: "Outline Robot"  # Optional commit author
git_email: "bot@domain.com"
purge_local_history: true  # Keep the local .git folders small

# Optional (defaults shown)
cleanup_exports: true      # Delete each export from Outline after downloading it
purge_old_exports: false   # Also delete exports left over from earlier runs (see below)
navigation: true           # Breadcrumbs, sub-page lists and "back to parent" links
export_timeout: 1800       # Seconds to wait for Outline to build an export

sync_tasks:
  - collection_name: "Knowledge Base"
    repo_url: "https://<user>:<github_token>@github.com/username/kb-backup.git"
    branch: "main"

  - collection_name: "Engineering"
    repo_url: "https://<user>:<github_token>@github.com/username/eng-docs.git"
    branch: "master"
```

`OUTLINE_URL` and `OUTLINE_TOKEN` environment variables override the values in the file, if you'd rather keep the token out of it. Set `TZ` (e.g. `TZ=Asia/Kolkata`) to get commit times in your timezone.

> **Note on Security:** Use a [GitHub Personal Access Token (PAT)](https://github.com/settings/tokens) in the `repo_url` for private repositories. Tokens in URLs are masked in the logs.

### Cleaning up exports in Outline

Outline keeps every export zip until it is deleted. Deleting exports through the API **requires an admin API token**. With a non-admin token the sync keeps working and logs a single warning, but you will have to delete exports yourself under *Settings → Export*.

To clear out exports left behind by older versions, set `purge_old_exports: true` once. Only **Markdown exports of the configured collections created by the token's user** are deleted. If you also make manual Markdown exports of these collections with the same account, those will be deleted too.

---

## Deployment

### 1. Build & Run

Ensure you have Docker and Docker Compose installed, then run:

```bash
docker compose up -d --build
```

### Upgrading from the Python version

Nothing to change: same `config.yml`, same volumes, same repo layout. Pull and rebuild:

```bash
git pull && docker compose up -d --build
```

The first sync after upgrading makes one extra commit, because every document gains the new navigation links.

### 2. File Structure

The container will maintain the following structure locally:

* `repo_data/`: Persistent volume containing the cloned git repositories (one folder per collection, spaces replaced by `_`).
* `config.yml`: Your settings and API keys.

---

##  Development

Requires Go 1.26+ and `git`.

```bash
go test ./...
go run . --config config.yml --repos ./repos --once
```

Flags: `--config` (env `CONFIG_PATH`, default `config.yml`), `--repos` (env `REPOS_DIR`, default `repos`), `--once` (single sync cycle, then exit).

---

##  How "Smart Sync" Works

1. **Exports** each collection from Outline as Markdown and waits for the zip to be ready.
2. **Downloads** the zip, then **deletes the export** from Outline.
3. **Fetches** the branch from the remote so local history matches GitHub.
4. **Extracts** the files into the local repo, replacing the previous contents.
5. **Adds navigation** to every document and regenerates `README.md`.
6. **Checks** `git diff`. If nothing but the README timestamp changed, it stops.
7. **Commits & Pushes** if changes exist, using the current timestamp as the message.
8. **Purges** local history (if enabled) to keep the `.git` folder tiny.

---

## Troubleshooting

* **Collection Not Found:** Make sure `collection_name` in `config.yml` matches the name in Outline. An exact match is tried first, then a case-insensitive one.
* **Permission Denied:** Verify your Outline API token has "Admin" or "Member" read access to the collections. Deleting exports needs Admin.
* **Push Failed:** Check that your GitHub PAT has `repo` scope permissions.
