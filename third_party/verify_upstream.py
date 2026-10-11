"""Verify scoped SDK provenance without network access or module-cache writes.

Default verifies copied file hashes/change lists and the patch hash. Providing
--archive additionally verifies the original Go zip, every upstream raw byte,
and that the recorded unified patch is exactly the baseline-to-copy diff.
"""
import argparse
import difflib
import hashlib
import json
from pathlib import Path
import zipfile


def sha(data):
    return hashlib.sha256(data).hexdigest()


def make_patch(baseline, current):
    chunks = []
    for name in sorted(set(baseline) | set(current)):
        before, after = baseline.get(name), current.get(name)
        if before == after:
            continue
        chunks.extend(difflib.unified_diff(
            [] if before is None else before.decode("utf-8").splitlines(True),
            [] if after is None else after.decode("utf-8").splitlines(True),
            fromfile="/dev/null" if before is None else "a/" + name,
            tofile="/dev/null" if after is None else "b/" + name))
    return "".join(chunks).encode("utf-8")


def verify(root, module, archive=None):
    copy = root / module["directory"]
    current = {p.relative_to(copy).as_posix(): p.read_bytes()
               for p in copy.rglob("*") if p.is_file()}
    upstream = {entry["path"]: entry for entry in module["upstreamFiles"]}
    changed = {entry["path"]: entry for entry in module["changedFiles"]}
    added = {entry["path"]: entry for entry in module["addedFiles"]}
    deleted = set(module["deletedFiles"])
    assert not (set(changed) & deleted), "change/delete overlap"
    assert set(changed) <= set(upstream), "change not in baseline"
    assert not (set(added) & set(upstream)), "added is upstream"
    assert set(current) == (set(upstream) - deleted) | set(added), "file set mismatch"
    assert deleted <= set(upstream), "deleted is not upstream"
    for name, content in current.items():
        entry = added.get(name) or changed.get(name) or upstream[name]
        expected = entry.get("patchedSHA256", entry.get("sha256"))
        assert sha(content) == expected, "copy hash mismatch: " + name
        if name not in changed:
            assert len(content) == entry["size"], "size mismatch: " + name
        else:
            assert entry["upstreamSHA256"] == upstream[name]["sha256"]
    patch = (root / module["patchFile"]).read_bytes()
    assert sha(patch) == module["patchSHA256"], "patch hash mismatch"
    if archive:
        archive = Path(archive)
        assert sha(archive.read_bytes()) == module["archiveSHA256"], "zip hash mismatch"
        prefix = module["module"] + "@" + module["version"] + "/"
        with zipfile.ZipFile(archive) as z:
            baseline = {name[len(prefix):]: z.read(name) for name in z.namelist()
                        if name.startswith(prefix) and not name.endswith("/")}
        assert set(baseline) == set(upstream), "archive file set mismatch"
        for name, content in baseline.items():
            assert sha(content) == upstream[name]["sha256"], "baseline hash: " + name
            assert len(content) == upstream[name]["size"]
        assert make_patch(baseline, current) == patch, "patch is not exact baseline diff"
    print(f'{module["module"]}@{module["version"]}: '
          f'{len(upstream)} baseline, {len(changed)} changed, {len(added)} added, '
          f'{len(deleted)} deleted; verified' + (" with archive" if archive else ""))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--module", default="go-mitmproxy")
    parser.add_argument("--archive", type=Path)
    args = parser.parse_args()
    manifest = json.loads((args.root / "third_party/upstream-manifest.json").read_text("utf-8"))
    matches = [m for m in manifest["modules"] if m["directory"].split("/")[-1] == args.module]
    assert len(matches) == 1, "unknown or duplicate module"
    verify(args.root, matches[0], args.archive)
