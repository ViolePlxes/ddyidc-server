import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("codeup_mirror", Path(__file__).with_name("codeup_mirror.py"))
mirror = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mirror)


class MirrorPolicyTests(unittest.TestCase):
    def test_credentials_are_bound_to_exact_host_and_repository(self):
        url = mirror.target_url("https://codeup.aliyun.com/org/ddyidc-server.git")
        good = "protocol=https\nhost=codeup.aliyun.com\npath=org/ddyidc-server.git\n"
        self.assertIn("password=dummy-token", mirror.credential(good, url, "dummy", "dummy-token"))
        for request in [good.replace("https", "http"), good.replace("codeup.aliyun.com", "other.example"),
                        good.replace("ddyidc-server", "another-repo"), "protocol=https\nhost=codeup.aliyun.com\n"]:
            self.assertEqual(mirror.credential(request, url, "dummy", "dummy-token"), "")

    def test_urls_cannot_embed_credentials_redirects_or_unapproved_hosts(self):
        for value in ["http://codeup.aliyun.com/o/r.git", "https://name:token@codeup.aliyun.com/o/r.git",
                      "https://codeup.aliyun.com.attacker.example/o/r.git", "https://codeup.aliyun.com/o/r.git?token=secret",
                      "https://codeup.aliyun.com/o/../r.git", "https://codeup.aliyun.com:8443/o/r.git"]:
            with self.assertRaises(ValueError):
                mirror.target_url(value)

    def test_latest_required_main_run_must_succeed(self):
        run = dict(id=1, head_sha="a" * 40, head_branch="main", event="push",
                   head_repository={"full_name": "ViolePlxes/ddyidc-server"},
                   path=".github/workflows/foundation-core.yml", status="completed", conclusion="success")
        check = lambda runs: mirror.successful_run(runs, "ViolePlxes/ddyidc-server", "a" * 40, "foundation-core.yml")
        self.assertTrue(check([run]))
        for changes in [dict(status="in_progress"), dict(conclusion="failure"), dict(conclusion="skipped"),
                        dict(event="pull_request"), dict(head_branch="feature"), dict(head_sha="b" * 40),
                        dict(head_repository={"full_name": "Other/repo"})]:
            self.assertFalse(check([{**run, **changes}]))
        self.assertFalse(check([run, {**run, "id": 2, "conclusion": "failure"}]))

    def test_all_workflows_are_required(self):
        sha = "a" * 40
        def api(repo, suffix):
            if suffix == "/commits/main":
                return {"sha": sha}
            name = suffix.split("/workflows/", 1)[1].split("/", 1)[0]
            return {"workflow_runs": [dict(id=1, head_sha=sha, head_branch="main", event="push",
                    head_repository={"full_name": repo}, path=".github/workflows/" + name,
                    status="completed", conclusion="failure" if name == "external-verification.yml" else "success")]}
        with patch.object(mirror, "api", side_effect=api):
            self.assertFalse(mirror.verified_main("ViolePlxes/ddyidc-server", sha))


class RealGitMirrorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.destination = self.root / "destination.git"
        subprocess.run(["git", "init", "-q", "-b", "main", str(self.source)], check=True)
        subprocess.run(["git", "init", "-q", "--bare", str(self.destination)], check=True)
        self.run_git("config", "user.email", "dummy@example.invalid")
        self.run_git("config", "user.name", "Mirror test")
        self.run_git("commit", "-qm", "initial", "--allow-empty")
        self.run_git("tag", "-a", "v0.1.1", "-m", "first")
        self.sha = self.run_git("rev-parse", "HEAD")
        self.real_git = mirror.git

    def run_git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.source), *args], text=True).strip()

    def local_git(self, *args, **kwargs):
        # All Git operations are real, and all writes stay in this test's tempdir.
        return self.real_git("-C", str(self.source), *args, **kwargs)

    def execute(self, verified=True, current=None):
        with patch.object(mirror, "target_url"), patch.object(mirror, "verified_main", return_value=verified), \
             patch.object(mirror, "api", return_value={"sha": current or self.sha}), \
             patch.object(mirror, "git", side_effect=self.local_git):
            mirror.mirror("ViolePlxes/ddyidc-server", str(self.destination))

    def test_real_tags_and_main_match_and_repeat_is_idempotent(self):
        self.execute()
        self.execute()
        refs = subprocess.check_output(["git", "--git-dir", str(self.destination), "show-ref"], text=True)
        self.assertIn(self.sha + " refs/heads/main", refs)
        self.assertIn(self.run_git("rev-parse", "refs/tags/v0.1.1") + " refs/tags/v0.1.1", refs)

    def test_historical_unverified_tag_is_not_newly_copied(self):
        self.run_git("commit", "-qm", "verified fix", "--allow-empty")
        self.sha = self.run_git("rev-parse", "HEAD")
        self.run_git("tag", "-a", "v0.1.2", "-m", "verified")
        self.execute()
        refs = subprocess.check_output(["git", "--git-dir", str(self.destination), "show-ref"], text=True)
        self.assertNotIn("refs/tags/v0.1.1", refs)
        self.assertIn("refs/tags/v0.1.2", refs)

    def test_failed_ci_does_not_publish(self):
        self.execute(verified=False)
        self.assertEqual(subprocess.check_output(["git", "--git-dir", str(self.destination), "for-each-ref"], text=True), "")

    def test_superseded_main_does_not_advance_remote_main(self):
        with self.assertRaises(RuntimeError):
            self.execute(current="b" * 40)
        self.assertEqual(subprocess.check_output(["git", "--git-dir", str(self.destination), "for-each-ref", "refs/heads"], text=True), "")

    def test_existing_divergent_tag_is_not_overwritten(self):
        self.execute()
        self.run_git("commit", "-qm", "second", "--allow-empty")
        self.sha = self.run_git("rev-parse", "HEAD")
        self.run_git("tag", "-f", "v0.1.1")
        with self.assertRaises(RuntimeError):
            self.execute()
        self.assertNotEqual(subprocess.check_output(["git", "--git-dir", str(self.destination), "rev-parse", "refs/tags/v0.1.1"], text=True).strip(), self.sha)


if __name__ == "__main__":
    unittest.main()
