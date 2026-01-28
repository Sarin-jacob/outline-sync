import os
import time
import subprocess
import requests
import zipfile
import io
import yaml
import shutil
from datetime import datetime

print("Starting Application", flush=True)

def run_git(args, cwd, verbose=True):
    """Runs git commands and flushes output for Docker logs."""
    result = subprocess.run(["git"] + args, capture_output=True, text=True, cwd=cwd)
    
    if result.returncode != 0:
        # We ignore errors for 'diff' or 'commit' when nothing is staged
        if "diff" not in args and "commit" not in args:
            print(f"--- GIT ERROR in {cwd} ---", flush=True)
            print(f"Command: git {' '.join(args)}", flush=True)
            print(f"Error: {result.stderr.strip()}", flush=True)
    elif verbose and result.stdout.strip():
        print(result.stdout.strip(), flush=True)
    return result

class OutlineSync:
    def __init__(self, config_path="config.yml"):
        print("Parsing Config", flush=True)
        with open(config_path, 'r') as f:
            self.config = yaml.safe_load(f)
        self.headers = {
            "Authorization": f"Bearer {self.config['outline_token']}",
            "Content-Type": "application/json"
        }
        self.base_url = self.config['outline_url'].rstrip('/')
        self.collections_cache = {}

    def setup_git_identity(self, repo_path):
        name = self.config.get('git_user', 'Outline Sync Bot')
        email = self.config.get('git_email', 'sync@jell0.online')
        # Removed extra single quotes around name/email
        run_git(["config", "user.name", name], repo_path, verbose=False)
        run_git(["config", "user.email", email], repo_path, verbose=False)

    def get_collection_id(self, name):
        if not self.collections_cache:
            try:
                res = requests.post(f"{self.base_url}/api/collections.list", json={}, headers=self.headers).json()
                if res.get("data"):
                    self.collections_cache = {c['name']: c['id'] for c in res['data']}
                else:
                    print("Error: Did not receive collection data from API", flush=True)
            except Exception as e:
                print(f"API Connection Error: {e}", flush=True)
        return self.collections_cache.get(name)

    def generate_readme(self, repo_path, collection_name):
        """Generates a README.md listing all markdown files in the collection folder."""
        # The zip extraction usually creates a folder named after the collection
        collection_folder = os.path.join(repo_path, collection_name)
        
        # If the folder doesn't exist (e.g. empty export), we fall back to repo root
        target_dir = collection_folder if os.path.exists(collection_folder) else repo_path
        
        readme_content = [f"# {collection_name} Index\n", "Last synced: " + datetime.now().strftime("%Y-%m-%d %H:%M:%S") + "\n\n## Documents\n"]
        
        # Walk only the first level of the collection directory
        try:
            items = os.listdir(target_dir)
            # Filter for .md files and exclude README.md itself
            md_files = [f for f in items if f.lower().endswith('.md') and f.lower() != 'readme.md']
            md_files.sort()

            if not md_files:
                readme_content.append("*No documents found.*")
            else:
                for md in md_files:
                    # Create a markdown link. Note: spaces in URLs need to be %20
                    safe_link = f"{collection_name}/{md}".replace(" ", "%20")
                    readme_content.append(f"* [{md.replace('.md', '')}]({safe_link})")
            
            with open(os.path.join(repo_path, "README.md"), "w") as f:
                f.write("\n".join(readme_content))
            print(f"Generated README.md for {collection_name}", flush=True)
            
        except Exception as e:
            print(f"Failed to generate README: {e}", flush=True)

    def process_task(self, task):
        name = task['collection_name']
        repo_path = os.path.abspath(f"./repos/{name.replace(' ', '_')}")
        branch = task.get('branch', 'main')
        
        print(f"[{datetime.now()}] Processing: {name}", flush=True)
        
        col_id = self.get_collection_id(name)
        if not col_id:
            print(f"Error: Could not find collection ID for '{name}'", flush=True)
            return

        if not os.path.exists(os.path.join(repo_path, ".git")):
            print(f"Initializing new repository at {repo_path}", flush=True)
            os.makedirs(repo_path, exist_ok=True)
            run_git(["init"], repo_path)
            run_git(["remote", "add", "origin", task['repo_url']], repo_path)
            run_git(["branch", "-M", branch], repo_path)
            self.setup_git_identity(repo_path)

        # 1. Export
        exp = requests.post(f"{self.base_url}/api/collections.export", json={"format": "outline-markdown","id": col_id}, headers=self.headers).json()
        if not exp.get("data"): 
            print(f"Export failed for {name}", flush=True)
            return
        
        fileops_id = exp['data']['fileOperation']['id']

        # 2. Wait for ZIP
        is_ready = False
        while not is_ready:
            t_res = requests.post(f"{self.base_url}/api/fileOperations.info", json={"id": fileops_id}, headers=self.headers).json()
            if t_res['data']['state'] == 'complete':
                is_ready = True
            elif t_res['data']['state'] == 'failed':
                print(f"File generation failed for {name}", flush=True)
                return
            else:
                time.sleep(5)

        # 3. Clean and Extract
        r = requests.post(f"{self.base_url}/api/fileOperations.redirect", json={"id": fileops_id}, headers=self.headers)
        r.raise_for_status()
        with zipfile.ZipFile(io.BytesIO(r.content)) as z:
            z.extractall(repo_path)

        # 4. Git Push
        # self.setup_git_identity(repo_path)
        self.generate_readme(repo_path, name)
        run_git(["add", "."], repo_path)

        content_diff = run_git(["diff", "--cached", "--quiet", "--", name], repo_path, verbose=False)
        
        if content_diff.returncode == 0:
            now_str = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
            run_git(["commit", "-m", f"Outline Sync: {now_str}"], repo_path)
            print(f"Changes committed. Pushing to {branch}...", flush=True)
            push_res = run_git(["push", "origin", branch], repo_path)
            if push_res.returncode == 0:
                print(f"Successfully pushed updates for {name}", flush=True)
            
            if self.config.get('purge_local_history', False):
                run_git(["gc", "--prune=now", "--aggressive"], repo_path)
        else:
            print(f"No changes detected for {name}", flush=True)

    def run(self):
        while True:
            self.collections_cache = {}
            for task in self.config['sync_tasks']:
                try:
                    self.process_task(task)
                except Exception as e:
                    print(f"Task Failed: {e}", flush=True)
            
            interval = self.config.get('check_interval', 3600)
            print(f"Sleeping for {interval}s...", flush=True)
            time.sleep(interval)

if __name__ == "__main__":
    OutlineSync().run()