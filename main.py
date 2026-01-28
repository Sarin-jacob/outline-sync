import os
import time
import subprocess
import requests
import zipfile
import io
import yaml
from datetime import datetime

def run_git(args, cwd):
    return subprocess.run(["git"] + args, capture_output=True, text=True, cwd=cwd)

class OutlineSync:
    def __init__(self, config_path="config.yml"):
        with open(config_path, 'r') as f:
            self.config = yaml.safe_load(f)
        self.headers = {
            "Authorization": f"Bearer {self.config['outline_token']}",
            "Content-Type": "application/json"
        }
        self.base_url = self.config['outline_url'].rstrip('/')
        self.collections_cache = {}

    def get_collection_id(self, name):
        if not self.collections_cache:
            res = requests.post(f"{self.base_url}/api/collections.list", json={}, headers=self.headers).json()
            if res.get("success"):
                self.collections_cache = {c['name']: c['id'] for c in res['data']}
        return self.collections_cache.get(name)

    def process_task(self, task):
        name = task['collection_name']
        repo_path = f"./repos/{name.replace(' ', '_')}"
        
        print(f"[{datetime.now()}] Processing: {name}")
        
        col_id = self.get_collection_id(name)
        if not col_id:
            print(f"Error: Could not find collection ID for '{name}'")
            return

        # Initialize Repo if needed
        if not os.path.exists(repo_path):
            os.makedirs(repo_path, exist_ok=True)
            subprocess.run(["git", "clone", task['repo_url'], repo_path])

        # 1. Export
        exp = requests.post(f"{self.base_url}/api/collections.export", json={"id": col_id}, headers=self.headers).json()
        if not exp.get("success"): return

        # 2. Wait for ZIP
        file_url = None
        while not file_url:
            t_res = requests.post(f"{self.base_url}/api/tasks.info", json={"id": exp['data']['id']}, headers=self.headers).json()
            if t_res['data']['state'] == 'complete':
                file_url = t_res['data']['result']['url']
            elif t_res['data']['state'] == 'failed': return
            else: time.sleep(5)

        # 3. Clean local and extract
        r = requests.get(file_url)
        with zipfile.ZipFile(io.BytesIO(r.content)) as z:
            z.extractall(repo_path)

        # 4. Smart Push
        run_git(["add", "."], repo_path)
        if run_git(["diff", "--cached", "--quiet"], repo_path).returncode != 0:
            now_str = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
            run_git(["commit", "-m", f"Outline Sync: {now_str}"], repo_path)
            run_git(["push", "origin", task.get('branch', 'main')], repo_path)
            print(f"Pushed updates for {name}")
        else:
            print(f"No changes for {name}")

    def run(self):
        while True:
            self.collections_cache = {} # Refresh IDs every loop
            for task in self.config['sync_tasks']:
                try:
                    self.process_task(task)
                except Exception as e:
                    print(f"Task Failed: {e}")
            time.sleep(self.config.get('check_interval', 3600))

if __name__ == "__main__":
    OutlineSync().run()