"""Run an isolated E2E installation against the disposable test PostgreSQL."""
import base64
import os
from pathlib import Path
import secrets
import signal
import subprocess

root = Path(__file__).resolve().parents[1]
schema = "e2e_" + secrets.token_hex(8)
postgres = ["docker", "compose", "-f", "compose.test.yaml", "exec", "-T", "postgres", "psql", "-U", "sherlock_test", "-d", "sherlock_test", "-v", "ON_ERROR_STOP=1", "-c"]
subprocess.run(postgres + [f'CREATE SCHEMA "{schema}"'], cwd=root, check=True, stdout=subprocess.DEVNULL)
child = None
demo_child = None
def stop(signum, frame):
    if child:
        child.terminate()
signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
try:
    (root / ".local").mkdir(exist_ok=True)
    subprocess.run(["go", "test", "-c", "-o", ".local/sherlock-e2e", "./internal/api"], cwd=root, check=True)
    env = os.environ | {
        "SHERLOCK_E2E_SERVER": "1",
        "DATABASE_URL": f"postgres://sherlock_test:local-test-only@127.0.0.1:55432/sherlock_test?sslmode=disable&search_path={schema}",
        "SHERLOCK_ALLOWED_CIDRS": "127.0.0.0/8",
        "SHERLOCK_NOTIFICATION_CIDRS": "127.0.0.0/8",
        "SHERLOCK_NOTIFICATION_HTTP_HOSTS": "127.0.0.1",
        "SHERLOCK_DEMO_URL": "http://127.0.0.1:19000",
        "SHERLOCK_ENCRYPTION_KEY": base64.b64encode(secrets.token_bytes(32)).decode(),
        "SHERLOCK_SETUP_TOKEN": "local-e2e-setup-token-not-for-real-deployments",
        "SHERLOCK_ORIGIN": "http://127.0.0.1:18080",
        "SHERLOCK_ADDR": "127.0.0.1:18080",
        "SHERLOCK_WEB_DIR": str(root / "web/dist"),
    }
    subprocess.run(["go", "build", "-o", ".local/sherlock-demo", "./cmd/demo"], cwd=root, check=True)
    demo_child = subprocess.Popen([str(root / ".local/sherlock-demo")], cwd=root, env=os.environ | {"SHERLOCK_DEMO_ADDR": "127.0.0.1:19000"})
    child = subprocess.Popen([str(root / ".local/sherlock-e2e"), "-test.run=^TestBrowserFixtureServer$", "-test.timeout=10m"], cwd=root, env=env)
    child.wait()
finally:
    if demo_child:
        demo_child.terminate()
        demo_child.wait(timeout=5)
    if child and child.poll() is None:
        child.terminate()
        child.wait(timeout=15)
    subprocess.run(postgres + [f'DROP SCHEMA "{schema}" CASCADE'], cwd=root, check=True, stdout=subprocess.DEVNULL)
