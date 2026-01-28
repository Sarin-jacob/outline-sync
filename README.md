#  Outline Git Sync

Automatically exports your self-hosted **Outline** collections as Markdown and syncs them to **Git** repositories.

##  Features

* **Smart Sync:** Commits and pushes only when actual changes are detected in your notes.
* **Storage Optimized:** Optional `purge_local_history` flag uses `git gc` to keep the container's disk footprint at a minimum.
* **Fast & Modern:** Built with **Python 3.12**, **Alpine Linux**, and **uv** for blazing-fast builds and execution.


---

##  Configuration (`config.yml`)

Create a `config.yml` in the root directory. You can add as many sync tasks as you need.

```yaml
outline_url: "https://docs.yourdomain.com"
outline_token: "outline_api_your_token_here"
check_interval: 3600      # Check for changes every hour (in seconds)
purge_local_history: true  # Aggressively cleans local .git folder to save space

sync_tasks:
  - collection_name: "Knowledge Base"
    repo_url: "https://<user>:<github_token>@github.com/username/kb-backup.git"
    branch: "main"
    
  - collection_name: "Engineering"
    repo_url: "https://<user>:<github_token>@github.com/username/eng-docs.git"
    branch: "master"
```

> **Note on Security:** Use a [GitHub Personal Access Token (PAT)](https://github.com/settings/tokens) in the `repo_url` for private repositories.


---

## Deployment

### 1. Build & Run

Ensure you have Docker and Docker Compose installed, then run:

```bash
docker compose up -d
```

### 2. File Structure

The container will maintain the following structure locally:

* `repo_data/`: Persistent volume containing the cloned git repositories.
* `config.yml`: Your settings and API keys.
* `main.py`: The sync logic.


---

##  Development (Using `uv`)

If you want to run the script locally without Docker:


1. **Install uv:** `curl -LsSf https://astral.sh/uv/install.sh | sh`
2. **Clone the repo**
3. **Run the script:**

```bash
uv run main.py
```

\n`*uv*` *will automatically create a virtual environment and install dependencies defined in the script header.*


---

##  How "Smart Sync" Works


1. **Polls** the Outline API for a collection export every X seconds.
2. **Downloads** the ZIP export into memory (avoiding disk bloat).
3. **Extracts** the files into the local git folder.
4. **Checks** `git diff`. If files are identical, it stops.
5. **Commits & Pushes** if changes exist, using the current timestamp as the message.
6. **Purges** local history (if enabled) to keep the `.git` folder tiny.


---

## Troubleshooting

* **Collection Not Found:** Ensure the `collection_name` in `config.yml` exactly matches the name in Outline (case-sensitive).
* **Permission Denied:** Verify your Outline API token has "Admin" or "Member" read access to the collections.
* **Push Failed:** Check that your GitHub PAT has `repo` scope permissions.