"""Publish verified GitHub main/releases to a private Codeup deployment mirror.

GitHub remains authoritative. No force pushes, deletions, credential files or
production SSH access are used. Credentials are supplied only through Git's
credential-helper pipe and must match the exact configured HTTPS repository.
"""
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import urllib.parse
import urllib.request

POLICIES = {
    "ViolePlxes/ddyidc-server": ("foundation-core.yml", "external-verification.yml"),
    "ViolePlxes/ddyidc-web": ("release-validation.yml",),
}
HOSTS = {"codeup.aliyun.com", "codeup.cn-hangzhou.aliyuncs.com", "ddyidc-cn-hangzhou.devops.aliyuncs.com"}


def target_url(value):
    url = urllib.parse.urlsplit(value)
    if (url.scheme != "https" or url.hostname not in HOSTS or url.port not in (None, 443)
            or url.username or url.password or url.query or url.fragment
            or not re.fullmatch(r"/[A-Za-z0-9_-]+(?:/[A-Za-z0-9_.-]+)+\.git", url.path)
            or any(p in (".", "..") for p in url.path.split("/"))):
        raise ValueError("Codeup URL must be an approved HTTPS repository without credentials")
    return url


def credential(request, url, username, token):
    fields = dict(line.split("=", 1) for line in request.splitlines() if "=" in line)
    if (fields.get("protocol") == "https" and fields.get("host") == url.netloc
            and fields.get("path", "").lstrip("/") == url.path.lstrip("/")):
        if any(c in username + token for c in "\r\n\0") or not username or not token:
            raise ValueError("Invalid credential fields")
        return "username=" + username + "\npassword=" + token + "\n\n"
    return ""


def api(repository, suffix):
    if repository not in POLICIES:
        raise ValueError("Repository is not approved")
    req = urllib.request.Request("https://api.github.com/repos/" + repository + suffix, headers={
        "Authorization": "Bearer " + os.environ["GITHUB_TOKEN"],
        "Accept": "application/vnd.github+json", "Cache-Control": "no-cache",
    })
    # GitHub API endpoints here must not redirect authenticated requests.
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args, **kwargs):
            return None
    with urllib.request.build_opener(NoRedirect).open(req, timeout=30) as response:
        return json.load(response)


def successful_run(runs, repository, sha, workflow):
    matches = [r for r in runs if r.get("head_sha") == sha and r.get("head_branch") == "main"
               and r.get("event") == "push"
               and r.get("head_repository", {}).get("full_name") == repository
               and r.get("path", "").split("@", 1)[0] == ".github/workflows/" + workflow]
    if not matches:
        return False
    latest = max(matches, key=lambda r: int(r["id"]))
    return latest.get("status") == "completed" and latest.get("conclusion") == "success"


def verified_main(repository, sha):
    if repository not in POLICIES or not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("Unapproved repository or commit")
    if api(repository, "/commits/main")["sha"] != sha:
        return False
    query = urllib.parse.urlencode({"branch": "main", "event": "push", "head_sha": sha, "per_page": 100})
    return all(successful_run(api(repository, "/actions/workflows/" + workflow + "/runs?" + query)
                              ["workflow_runs"], repository, sha, workflow)
               for workflow in POLICIES[repository])


def git(*args, authenticated=False):
    env = {k: v for k, v in os.environ.items() if not k.startswith(("GIT_TRACE", "GIT_CONFIG_"))
           and k not in ("GIT_CURL_VERBOSE", "GIT_ASKPASS", "SSH_ASKPASS")}
    env["GIT_TERMINAL_PROMPT"] = "0"
    options = ["-c", "core.hooksPath=/dev/null", "-c", "http.followRedirects=false",
               "-c", "http.sslVerify=true", "-c", "credential.helper=", "-c", "credential.useHttpPath=true"]
    if authenticated:
        options += ["-c", "credential.helper=!python3 " + shlex.quote(str(Path(__file__).resolve())) + " credential"]
    result = subprocess.run(["git", *options, *args], env=env, capture_output=True, text=True, timeout=180)
    if result.returncode:
        # Git output can contain remote-service text. Never print raw output or secrets.
        raise RuntimeError("Git operation failed: " + args[0] + " (exit " + str(result.returncode) + ")")
    return result.stdout.strip()


def release_tags(sha):
    tags = []
    for tag in git("tag", "--list", "v0.1.*").splitlines():
        if not re.fullmatch(r"v0\.1\.\d+", tag):
            continue
        commit = git("rev-parse", "refs/tags/" + tag + "^{commit}")
        # Only tags at this CI-verified commit are newly published. Historical
        # tags may point at commits whose own checks failed; do not copy them.
        if commit == sha:
            tags.append(tag)
    return tags


def remote_refs(url):
    return dict((ref, value) for value, ref in
                (line.split("\t", 1) for line in git("ls-remote", url, authenticated=True).splitlines()))


def mirror(repository, url):
    target_url(url)
    sha = git("rev-parse", "HEAD")
    if not verified_main(repository, sha):
        print(json.dumps({"status": "waiting_or_superseded", "commit": sha}))
        return
    if git("status", "--porcelain"):
        raise RuntimeError("Refusing a dirty checkout")
    tags = release_tags(sha)
    before = remote_refs(url)
    for tag in tags:
        ref = "refs/tags/" + tag
        if ref in before and before[ref] != git("rev-parse", ref):
            raise RuntimeError("An existing release tag differs; refusing overwrite")
    # Tags first, main last: an interrupted transfer never advertises a new main
    # before its version metadata. Existing tags and main are never forced.
    missing = ["refs/tags/" + t + ":refs/tags/" + t for t in tags if "refs/tags/" + t not in before]
    if missing:
        git("push", url, *missing, authenticated=True)
    if api(repository, "/commits/main")["sha"] != sha:
        raise RuntimeError("GitHub main changed; refusing obsolete mirror publication")
    git("push", url, "HEAD:refs/heads/main", authenticated=True)
    after = remote_refs(url)
    if after.get("refs/heads/main") != sha or any(after.get("refs/tags/" + t) != git("rev-parse", "refs/tags/" + t) for t in tags):
        raise RuntimeError("Mirror verification failed")
    print(json.dumps({"status": "verified", "commit": sha, "releaseTags": len(tags)}))


def main():
    url = os.environ["CODEUP_REPOSITORY_URL"]
    parsed = target_url(url)
    if len(sys.argv) > 1 and sys.argv[1] == "credential":
        if len(sys.argv) > 2 and sys.argv[2] == "get":
            sys.stdout.write(credential(sys.stdin.read(), parsed, os.environ["CODEUP_USERNAME"], os.environ["CODEUP_TOKEN"]))
        return
    mirror(os.environ["GITHUB_REPOSITORY"], url)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("Codeup mirror stopped: " + type(error).__name__, file=sys.stderr)
        raise SystemExit(1)
