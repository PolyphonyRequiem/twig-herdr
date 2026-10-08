#!/usr/bin/env python3
"""Build-independent, standard-library release packaging and publication tools."""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import tomllib
import zipfile


ROOT = Path(__file__).resolve().parent.parent
PRODUCTS = ("twig-herdr", "twig-bench-tui")
PLATFORMS = {
    "windows-amd64": ("win-x64", ".exe", "zip", "e_sqlite3.dll"),
    "linux-amd64": ("linux-x64", "", "tar.gz", "libe_sqlite3.so"),
    "linux-arm64": ("linux-arm64", "", "tar.gz", "libe_sqlite3.so"),
    "macos-amd64": ("osx-x64", "", "tar.gz", "libe_sqlite3.dylib"),
    "macos-arm64": ("osx-arm64", "", "tar.gz", "libe_sqlite3.dylib"),
}
CAPABILITIES = (
    (("workspace",), ("--include-browser", "--expect-binding", "--expect-identity")),
    (("bench", "configuration"), ("--expect-bench", "--expect-binding", "--expect-identity")),
    (("bench", "detail"), ("--width", "--expect-bench", "--expect-binding", "--expect-identity")),
    (("bench", "configuration", "area", "candidates"), ("--expect-bench", "--expect-binding", "--expect-identity")),
    (("bench", "configuration", "area", "add"), ("--expect-bench", "--expect-binding", "--expect-identity", "--expect-settings")),
    (("bench", "configuration", "sprint", "remove"), ("--expect-bench", "--expect-binding", "--expect-identity", "--expect-settings")),
    (("workspace", "track"), ("--expect-bench", "--expect-binding", "--expect-identity", "--expect-settings")),
    (("workspace", "track-tree"), ("--expect-bench", "--expect-binding", "--expect-identity", "--expect-settings")),
    (("workspace", "untrack"), ("--mode", "--expect-bench", "--expect-binding", "--expect-identity", "--expect-settings")),
    (("workspace", "sync"), ("--expect-bench", "--expect-binding", "--expect-identity")),
    (("bench", "list"), ("--include-management", "--expect-binding", "--expect-identity")),
    (("bench", "create"), ("--expect-binding", "--expect-identity")),
    (("bench", "switch"), ("--expect-bench", "--expect-binding", "--expect-identity")),
    (("bench", "delete"), ("--expect-bench", "--expect-binding", "--expect-identity", "--expect-contents")),
)
SEMVER = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?")


def source_pin():
    pin = json.loads((ROOT / "scripts/native-source.json").read_text(encoding="utf-8"))
    if pin.get("schemaVersion") != 1 or not re.fullmatch(r"[0-9a-f]{40}", pin.get("commit", "")):
        raise ValueError("native-source.json must pin an exact source commit")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", pin.get("repository", "")):
        raise ValueError("native-source.json contains an invalid repository")
    if not pin.get("ref") or not pin.get("dotnetSdk"):
        raise ValueError("native-source.json must record the source ref and tested SDK")
    return pin


def check_version(version):
    manifest = tomllib.loads((ROOT / "herdr-plugin.toml").read_text(encoding="utf-8"))
    if not SEMVER.fullmatch(version) or manifest["version"] != version:
        raise ValueError(f"release version {version!r} does not match herdr-plugin.toml {manifest['version']!r}")
    return version


def json_bytes(value):
    return (json.dumps(value, indent=2, sort_keys=True, ensure_ascii=False) + "\n").encode("utf-8")


def sha256(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def run(command, *, cwd=None, env=None, timeout=120):
    result = subprocess.run(command, cwd=cwd, env=env, text=True, encoding="utf-8", errors="replace",
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"command failed ({result.returncode}): {' '.join(map(str, command))}\n{result.stdout}{result.stderr}")
    return result.stdout.strip()


def prepare(args):
    if not args.tag.startswith("v"):
        raise ValueError("release tag must start with v")
    version = check_version(args.tag[1:])
    pin = source_pin()
    values = {"version": version, "native_repository": pin["repository"], "native_ref": pin["ref"],
              "native_commit": pin["commit"], "dotnet_sdk": pin["dotnetSdk"]}
    if args.github_output:
        with Path(args.github_output).open("a", encoding="utf-8", newline="\n") as stream:
            stream.writelines(f"{key}={value}\n" for key, value in values.items())
    if args.github_env:
        metadata_home = (Path(os.environ["RUNNER_TEMP"]) / "twig-bench-release-metadata").resolve()
        metadata_home.mkdir(parents=True, exist_ok=True)
        with Path(args.github_env).open("a", encoding="utf-8", newline="\n") as stream:
            stream.write(f"TWIG_USER_HOME={metadata_home}\n")
    print(json.dumps(values, sort_keys=True))


def verify_native(args):
    pin = source_pin()
    checkout = Path(args.source).resolve()
    commit = run(["git", "rev-parse", "HEAD"], cwd=checkout)
    if commit != pin["commit"]:
        raise ValueError(f"native checkout {commit} does not match pinned {pin['commit']}")
    sdk = run(["dotnet", "--version"], cwd=checkout)
    if sdk != pin["dotnetSdk"]:
        raise ValueError(f"native checkout selected SDK {sdk}; expected tested {pin['dotnetSdk']}")
    print(f"Native source verified: {pin['repository']}@{commit}, SDK {sdk}")


def smoke(frontends, native, version):
    # Help/version are offline; no ambient workspace, credentials, telemetry or metadata writes.
    with tempfile.TemporaryDirectory(prefix="twig-bench-release-smoke-") as temporary:
        cwd = Path(temporary)
        env = dict(os.environ, TWIG_USER_HOME=str(cwd / "metadata"), TWIG_TELEMETRY_ENDPOINT="",
                   NO_COLOR="1", TERM="dumb")
        for product, binary in frontends.items():
            actual = run([str(binary.resolve()), "--version"], cwd=cwd, env=env)
            if actual != version:
                raise ValueError(f"{product} reports {actual!r}, expected {version!r}")
            help_text = run([str(binary.resolve()), "--help"], cwd=cwd, env=env)
            required_help = "twig-herdr open" if product == "twig-herdr" else "--cwd"
            if product not in help_text or required_help not in help_text:
                raise ValueError(f"{product} does not expose its browser CLI help")
        native_version = run([str(native.resolve()), "--version"], cwd=cwd, env=env)
        if not SEMVER.fullmatch(native_version):
            raise ValueError(f"native companion reports an invalid version: {native_version!r}")
        for command, required in CAPABILITIES:
            help_text = run([str(native.resolve()), *command, "--help"], cwd=cwd, env=env)
            missing = [option for option in required if option not in help_text]
            if missing:
                raise ValueError(f"native {' '.join(command)} lacks capabilities: {', '.join(missing)}")
        print(f"Offline smoke passed: {', '.join(frontends)} {version}, native {native_version}")
        return native_version


def install_text(product, platform, version, pin):
    _, ext, _, _ = PLATFORMS[platform]
    binary = product + ext
    lines = [
        f"{product} v{version} portable bundle ({platform})", "",
        "Extract the whole archive into one directory; there is no enclosing version directory.",
        f"Keep {binary} beside twig-bench-native/ and keep all files inside that companion directory.",
        "No Go or .NET SDK/runtime installation is required.",
        "The companion is private to this browser: do not rename it to twig, put it on PATH,",
        "replace normal Twig or its shims, or copy only the browser executable.", "",
        "Verify the downloaded archive against this release's SHA256SUMS before extracting.",
    ]
    if ext:
        lines.extend(["PowerShell checksum: Get-FileHash -Algorithm SHA256 .\\<downloaded-archive>.zip",
                      "Compare the hash with the exact filename in SHA256SUMS."])
    else:
        command = "shasum -a 256" if platform.startswith("macos-") else "sha256sum"
        lines.extend([f"Checksum: {command} <downloaded-archive>.tar.gz",
                      "Compare the hash with the exact filename in SHA256SUMS."])
    lines.extend(["", "Run from an existing, initialized Twig workspace in a real terminal.",
                  "Installation does not migrate, initialize, or select your workspace or credentials.",
                  "If native host admission refuses a legacy workspace, use your normal Twig CLI's",
                  "explicit connection migration workflow only after reviewing its preview.", ""])
    if product == "twig-bench-tui":
        lines.extend([f"PowerShell: .\\{binary} --cwd C:\\path\\to\\workspace" if ext else
                      f"Terminal: ./{binary} --cwd /path/to/workspace", "",
                      "Optional download/install (default destination: ~/.local/bin):",
                      "Download install.ps1 (Windows) or install.sh (Unix) from the same release and",
                      "verify the script against SHA256SUMS before running it."])
        lines.extend([f"PowerShell: powershell -NoProfile -ExecutionPolicy Bypass -File .\\install.ps1 -Product twig-bench-tui -Version v{version}",
                      "Latest: powershell -NoProfile -ExecutionPolicy Bypass -File .\\install.ps1 -Product twig-bench-tui -Version latest"] if ext else
                     [f"Unix: sh install.sh --product twig-bench-tui --version v{version}",
                      "Latest: sh install.sh --product twig-bench-tui --version latest"])
    else:
        lines.extend(["Requires Herdr 0.9.0 or newer for the extension UI.",
                      "Install/register the twig-herdr plugin normally with Herdr; its installer",
                      "downloads and checksums this complete bundle into the plugin's bin directory.",
                      f"Portable help: .\\{binary} --help" if ext else f"Portable help: ./{binary} --help",
                      "Use twig-herdr open from a source Herdr pane to open the browser."])
    lines.extend(["", "Normal Twig on PATH is still needed for digest-guarded proposal review and",
                  "explicit workspace setup; the bundled companion supplies semantic Bench reads,",
                  "captured-origin/configuration/lifecycle guards and scoped Bench sync.",
                  "The browser never silently switches your existing connection or principal.", "",
                  f"Native source: https://github.com/{pin['repository']}/commit/{pin['commit']}",
                  f"Native source ref: {pin['ref']}", f"Build SDK: {pin['dotnetSdk']}", ""])
    return "\n".join(lines).encode("utf-8")


def native_files(directory, ext, sqlite_name):
    directory = directory.resolve()
    candidates = [directory / (name + ext) for name in ("twig", "twig-bench-native")]
    binaries = [path for path in candidates if path.is_file()]
    if len(binaries) != 1 or not (directory / sqlite_name).is_file():
        raise ValueError(f"native publish must contain exactly one twig{ext}/twig-bench-native{ext} and {sqlite_name}")
    files = {}
    for path in sorted(directory.rglob("*")):
        relative = path.relative_to(directory)
        if any(part.endswith(".dSYM") for part in relative.parts) or path.suffix.lower() in (".pdb", ".dbg", ".debug"):
            continue
        if path.is_symlink():
            raise ValueError(f"native publish contains an unsupported symlink: {relative}")
        if not path.is_file():
            continue
        name = "twig-bench-native" + ext if path == binaries[0] else relative.as_posix()
        files["twig-bench-native/" + name] = path
    return binaries[0], files


def archive(path, entries):
    # Stable names/order, timestamps, owner IDs and modes; no source mtimes or absolute paths.
    if path.suffix == ".zip":
        with zipfile.ZipFile(path, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=9) as output:
            for name, (content, mode) in sorted(entries.items()):
                entry = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
                entry.create_system = 3
                entry.external_attr = (stat.S_IFREG | mode) << 16
                entry.compress_type = zipfile.ZIP_DEFLATED
                output.writestr(entry, content, compresslevel=9)
    else:
        with path.open("wb") as raw:
            with gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0, compresslevel=9) as compressed:
                with tarfile.open(fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT) as output:
                    for name, (content, mode) in sorted(entries.items()):
                        entry = tarfile.TarInfo(name)
                        entry.size = len(content)
                        entry.mode = mode
                        entry.mtime = 0
                        entry.uid = entry.gid = 0
                        entry.uname = entry.gname = ""
                        output.addfile(entry, io.BytesIO(content))


def write_sums(directory, names):
    (directory / "SHA256SUMS").write_text("".join(f"{sha256(directory / name)}  {name}\n" for name in sorted(names)),
                                        encoding="ascii", newline="\n")


def package(args):
    version = check_version(args.version)
    pin = source_pin()
    rid, ext, archive_ext, sqlite_name = PLATFORMS[args.platform]
    frontend_dir = Path(args.frontend_dir).resolve()
    frontends = {product: frontend_dir / (product + ext) for product in PRODUCTS}
    for binary in frontends.values():
        if not binary.is_file():
            raise ValueError(f"missing frontend: {binary}")
    native, dependencies = native_files(Path(args.native_dir), ext, sqlite_name)
    native_version = smoke(frontends, native, version)
    out = Path(args.output).resolve()
    out.mkdir(parents=True, exist_ok=True)
    assets = {}
    for product, frontend in frontends.items():
        raw_name = f"{product}-{args.platform}{ext}"
        shutil.copyfile(frontend, out / raw_name)
        if not ext:
            (out / raw_name).chmod(0o755)
        entries = {product + ext: (frontend.read_bytes(), 0o755),
                   "INSTALL.txt": (install_text(product, args.platform, version, pin), 0o644)}
        for name, dependency in dependencies.items():
            mode = 0o755 if dependency.suffix.lower() in (".exe", ".dll", ".so", ".dylib") or dependency == native else 0o644
            entries[name] = (dependency.read_bytes(), mode)
        archive_name = f"{product}-{args.platform}.{archive_ext}"
        archive(out / archive_name, entries)
        # Exercise the renamed private helper from the exact delivered directory layout.
        with tempfile.TemporaryDirectory(prefix="twig-bench-bundle-") as temporary:
            extracted = Path(temporary)
            if archive_ext == "zip":
                with zipfile.ZipFile(out / archive_name) as bundle:
                    bundle.extractall(extracted)
            else:
                with tarfile.open(out / archive_name, "r:gz") as bundle:
                    bundle.extractall(extracted, filter="data")
            packaged_frontend = extracted / (product + ext)
            packaged_native = extracted / "twig-bench-native" / ("twig-bench-native" + ext)
            if not ext:
                packaged_frontend.chmod(0o755)
                packaged_native.chmod(0o755)
            smoke({product: packaged_frontend}, packaged_native, version)
        for name in (raw_name, archive_name):
            assets[name] = sha256(out / name)
    build_manifest = {"schemaVersion": 1, "version": version, "platform": args.platform, "rid": rid,
                      "nativeSource": pin, "nativeVersion": native_version, "assets": assets,
                      "nativeFiles": {name: sha256(path) for name, path in sorted(dependencies.items())}}
    manifest_name = f"build-{args.platform}.json"
    (out / manifest_name).write_bytes(json_bytes(build_manifest))
    write_sums(out, [*assets, manifest_name])
    print(f"Packaged {args.platform}: {', '.join(sorted(assets))}")


def assemble(args):
    version = check_version(args.version)
    pin = source_pin()
    artifacts = Path(args.artifacts).resolve()
    out = Path(args.output).resolve()
    if out.exists() and any(out.iterdir()):
        raise ValueError("assemble requires an empty output directory")
    out.mkdir(parents=True, exist_ok=True)
    builds = {}
    assets = []
    for platform, (rid, ext, archive_ext, _) in PLATFORMS.items():
        manifests = list(artifacts.rglob(f"build-{platform}.json"))
        if len(manifests) != 1:
            raise ValueError(f"expected exactly one build manifest for {platform}, found {len(manifests)}")
        manifest = json.loads(manifests[0].read_text(encoding="utf-8"))
        if (manifest.get("schemaVersion"), manifest.get("version"), manifest.get("platform"), manifest.get("rid"), manifest.get("nativeSource")) != (1, version, platform, rid, pin):
            raise ValueError(f"build metadata disagrees with the release pin/version/platform: {platform}")
        names = {name for product in PRODUCTS for name in
                 (f"{product}-{platform}{ext}", f"{product}-{platform}.{archive_ext}")}
        if set(manifest["assets"]) != names:
            raise ValueError(f"incomplete assets for {platform}")
        for name in sorted(names):
            source = manifests[0].parent / name
            if not source.is_file() or sha256(source) != manifest["assets"][name]:
                raise ValueError(f"missing or changed build asset: {name}")
            shutil.copyfile(source, out / name)
            assets.append(name)
        builds[platform] = manifest
    for name in ("install.ps1", "install.sh"):
        shutil.copyfile(ROOT / "scripts" / name, out / name)
        assets.append(name)
    release_manifest = {"schemaVersion": 1, "version": version, "nativeSource": pin,
                        "products": list(PRODUCTS), "builds": builds}
    (out / "release-manifest.json").write_bytes(json_bytes(release_manifest))
    assets.append("release-manifest.json")
    write_sums(out, assets)
    print(f"Assembled {len(assets) + 1} downloadable assets in {out}")


def publish(args):
    version = check_version(args.tag.removeprefix("v"))
    if args.tag != "v" + version:
        raise ValueError("release tag must start with v")
    directory = Path(args.directory).resolve()
    sums = directory / "SHA256SUMS"
    if not sums.is_file():
        raise ValueError("release directory has no SHA256SUMS")
    expected = {}
    for line in sums.read_text(encoding="ascii").splitlines():
        digest, name = line.split("  ", 1)
        if not re.fullmatch(r"[0-9a-f]{64}", digest) or Path(name).name != name or name in expected:
            raise ValueError("malformed or duplicate SHA256SUMS entry")
        if sha256(directory / name) != digest:
            raise ValueError(f"release checksum mismatch: {name}")
        expected[name] = digest
    if set(path.name for path in directory.iterdir()) != {*expected, "SHA256SUMS"}:
        raise ValueError("release directory contains assets not covered by SHA256SUMS")
    manifest = json.loads((directory / "release-manifest.json").read_text(encoding="utf-8"))
    if manifest["version"] != version or manifest["nativeSource"] != source_pin() or set(manifest["builds"]) != set(PLATFORMS):
        raise ValueError("release metadata is incomplete or disagrees with the frozen source pin")
    base = ["--repo", args.repository]
    existing = subprocess.run(["gh", "release", "view", args.tag, *base, "--json", "isDraft,assets"],
                              text=True, encoding="utf-8", stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if existing.returncode == 0:
        state = json.loads(existing.stdout)
        if not state["isDraft"]:
            raise ValueError("refusing to modify an already published release")
        for asset in state["assets"]:
            if asset["name"] not in {*expected, "SHA256SUMS"}:
                run(["gh", "release", "delete-asset", args.tag, asset["name"], *base, "--yes"])
    elif "release not found" in existing.stderr.lower() or "HTTP 404" in existing.stderr:
        notes = ("Portable Twig Herdr extension and standalone Twig Bench TUI bundles include the private "
                 "native companion and SQLite dependency. Download the bundle for your platform, verify "
                 "SHA256SUMS, and follow INSTALL.txt. Raw binaries are compatibility assets, not complete "
                 "installations. Source/SDK provenance is pinned in release-manifest.json. "
                 "No normal Twig CLI release, shim replacement or workspace migration is performed.")
        run(["gh", "release", "create", args.tag, *base, "--draft", "--verify-tag", "--target", args.commit,
             "--title", args.tag, "--generate-notes", "--notes", notes])
    else:
        raise RuntimeError(f"could not inspect release: {existing.stderr}")
    files = [directory / name for name in sorted([*expected, "SHA256SUMS"])]
    run(["gh", "release", "upload", args.tag, *map(str, files), *base, "--clobber"], timeout=1800)
    state = json.loads(run(["gh", "release", "view", args.tag, *base, "--json", "isDraft,assets"]))
    if not state["isDraft"] or {asset["name"] for asset in state["assets"]} != {*expected, "SHA256SUMS"}:
        raise ValueError("draft release asset inventory is incomplete or changed during upload")
    # Download and hash the actual GitHub assets before making this release visible as latest.
    with tempfile.TemporaryDirectory(prefix="twig-bench-published-assets-") as temporary:
        downloaded = Path(temporary)
        run(["gh", "release", "download", args.tag, *base, "--dir", temporary], timeout=1800)
        for name, digest in expected.items():
            if sha256(downloaded / name) != digest:
                raise ValueError(f"uploaded release checksum mismatch: {name}; release remains draft")
        if (downloaded / "SHA256SUMS").read_bytes() != sums.read_bytes():
            raise ValueError("uploaded SHA256SUMS changed; release remains draft")
    flags = ["--draft=false", "--latest=true"]
    if "-" in version:
        flags = ["--draft=false", "--prerelease", "--latest=false"]
    run(["gh", "release", "edit", args.tag, *base, *flags])
    print(f"Published complete release {args.repository}@{args.tag}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    command = commands.add_parser("prepare", help="verify version and expose source pin to Actions")
    command.add_argument("--tag", required=True)
    command.add_argument("--github-output")
    command.add_argument("--github-env")
    command.set_defaults(action=prepare)
    command = commands.add_parser("verify-native", help="verify exact native checkout and selected SDK")
    command.add_argument("--source", required=True)
    command.set_defaults(action=verify_native)
    command = commands.add_parser("package", help="smoke binaries and make deterministic platform assets")
    command.add_argument("--platform", choices=PLATFORMS, required=True)
    command.add_argument("--version", required=True)
    command.add_argument("--frontend-dir", required=True)
    command.add_argument("--native-dir", required=True)
    command.add_argument("--output", required=True)
    command.set_defaults(action=package)
    command = commands.add_parser("assemble", help="verify all five platform artifacts and aggregate assets")
    command.add_argument("--version", required=True)
    command.add_argument("--artifacts", required=True)
    command.add_argument("--output", required=True)
    command.set_defaults(action=assemble)
    command = commands.add_parser("publish", help="upload, verify and finally publish a complete draft release")
    command.add_argument("--tag", required=True)
    command.add_argument("--repository", required=True)
    command.add_argument("--commit", required=True)
    command.add_argument("--directory", required=True)
    command.set_defaults(action=publish)
    args = parser.parse_args()
    args.action(args)


if __name__ == "__main__":
    main()
