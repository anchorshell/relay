"""Explicit artifact setup; never imported by normal tests or the worker."""
import hashlib
import json
import argparse
import os
import shutil
from pathlib import Path
from huggingface_hub import snapshot_download

ROOT = Path(__file__).resolve().parent

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cached-only", action="store_true", help="Prepare the exact pinned snapshot from the existing Hub cache, without network access")
    parser.add_argument("--destination", type=Path, default=ROOT / ".cache" / "model", help="Persistent checkpoint destination; serving never downloads")
    args = parser.parse_args()
    manifest = json.loads((ROOT / "model.json").read_text())
    target = args.destination.resolve()
    if args.cached_only:
        snapshot = Path(snapshot_download(manifest["repository"], revision=manifest["revision"], local_files_only=True))
        required = ["model.safetensors", "rl_agent_config.json", "encoder/config.json", "tokenizer/tokenizer.json", "tokenizer/tokenizer_config.json"]
        if any(not (snapshot / name).is_file() for name in required):
            raise RuntimeError("Pinned snapshot is incomplete in the local cache; no download attempted")
        for source in snapshot.rglob("*"):
            if not source.is_file():
                continue
            destination = target / source.relative_to(snapshot)
            destination.parent.mkdir(parents=True, exist_ok=True)
            if source.name == "model.safetensors":
                # No duplicate weight download or disk copy. Never mutate weights.
                if destination.exists():
                    if not os.path.samefile(source, destination):
                        raise RuntimeError("Existing weights differ from cached source; choose a clean setup directory")
                else:
                    os.link(source.resolve(), destination)
            else:
                # Tokenizer normalization must not mutate the shared Hub cache.
                shutil.copyfile(source, destination)
    else:
        snapshot_download(manifest["repository"], revision=manifest["revision"], local_dir=target,
            allow_patterns=["model.safetensors", "rl_agent_config.json", "encoder/config.json", "tokenizer/*", "README.md", "LICENSE*", "NOTICE*"])
    # Apply the pinned runtime's compatibility normalization during setup,
    # before checksumming; serving does not need to mutate the artifacts.
    from laya.agent import _fix_tokenizer_config
    _fix_tokenizer_config(str(target))
    checksums = {}
    for path in sorted(target.rglob("*")):
        if path.is_file() and path.name != "anchorshell-integrity.json" and ".cache" not in path.relative_to(target).parts:
            with path.open("rb") as source:
                checksums[str(path.relative_to(target))] = hashlib.file_digest(source, "sha256").hexdigest()
    (target / "anchorshell-integrity.json").write_text(json.dumps({"revision": manifest["revision"], "files": checksums}, indent=2) + "\n")
    print("Pinned Laya checkpoint prepared. No inference or training was run.")

if __name__ == "__main__":
    main()
