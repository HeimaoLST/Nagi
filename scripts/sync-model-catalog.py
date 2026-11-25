#!/usr/bin/env python3
from copy import deepcopy
import hashlib
import json
import sys
import tarfile
import tomllib
from pathlib import Path


COMMIT = "066eb88a4dbbe15d5274ad5528070ce5112bbe18"
ARCHIVE_SHA256 = "e933d26d36b6919db0c0e6b4b151c881b9a37b56669c5020fb7751e39615ab48"
PROVIDERS = (
    "alibaba", "alibaba-cn", "baseten", "cerebras", "deepseek", "github-copilot", "groq", "huggingface",
    "meta", "minimax", "mistral", "moonshotai", "moonshotai-cn", "nvidia", "openai", "opencode",
    "opencode-go", "openrouter", "xiaomi", "xai", "zai", "zhipuai",
)
WIRE_APIS = {"chat-completions": "openai-completions", "responses": "openai-responses"}
THINKING_LEVELS = ("none", "low", "medium", "high")


def merge(base, overrides):
    result = deepcopy(base)
    for key, value in overrides.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = merge(result[key], value)
        else:
            result[key] = value
    return result


def omit(data, path):
    key, _, rest = path.partition(".")
    if not rest:
        data.pop(key, None)
    elif isinstance(data.get(key), dict):
        omit(data[key], rest)


def project_model(provider, model, source, data, default_api, requires_builtin):
    apis = data.get("api", {}) or {default_api: {}}
    modalities = data.get("modalities", {})
    is_chat = data.get("type", "chat") == "chat" and "text" in modalities.get("output", ["text"])
    digest = hashlib.sha256(source).hexdigest()
    for api, wire in WIRE_APIS.items():
        row = {"source": provider, "id": model, "api": wire, "rawSha256": digest, "chat": is_chat and api in apis}
        if requires_builtin:
            row["requiresBuiltin"] = True
        if row["chat"]:
            transport = apis[api]
            flags = merge(data.get("capabilities", {}), transport.get("capabilities", {}))
            reasoning = flags.get("reasoning", {})
            for field in ("tools", "reasoning"):
                supported = flags.get(field, {}).get("supported")
                if isinstance(supported, bool):
                    row[field] = supported
            efforts = reasoning.get("effort", [])
            if reasoning.get("toggle") is True:
                efforts = [*efforts, "none"]
            if efforts:
                row["thinkingLevels"] = [level for level in THINKING_LEVELS if level in efforts]
            limits = {**data.get("limit", {}), **transport.get("limit", {})}
            for field in ("context", "output"):
                value = limits.get(field)
                if isinstance(value, int) and not isinstance(value, bool) and value > 0:
                    row[field] = value
            for direction in ("input", "output"):
                price = data.get("cost", {}).get("text", {}).get(direction, {}).get("price")
                if isinstance(price, (int, float)) and not isinstance(price, bool) and price >= 0:
                    row[f"{direction}Price"] = price
            if provider == "openai" and model == "gpt-6.2-sol" and api == "chat-completions" and "none" not in efforts:
                row["tools"] = False
            if isinstance(flags.get("system_updates"), bool):
                row["midHistorySystem"] = flags["system_updates"]
            if provider == "deepseek" and api == "chat-completions":
                compat = transport.get("compatibility", {})
                if compat.get("reasoning_field") == "reasoning_content":
                    row["compat"] = {"thinkingFormat": "deepseek", "requiresReasoningContentOnAssistantMessages": True}
                    if compat.get("max_tokens_field") in ("max_tokens", "max_completion_tokens"):
                        row["compat"]["maxTokensField"] = compat["max_tokens_field"]
        yield row


def project(archive):
    root = f"models.dev-{COMMIT}/"
    labs = {}
    providers = {provider: {} for provider in PROVIDERS}
    for member in archive:
        if not member.isfile() or not member.name.endswith(".toml") or not member.name.startswith(root):
            continue
        section, _, relative = member.name[len(root):].partition("/")
        if section == "models":
            labs[relative.removesuffix(".toml")] = archive.extractfile(member).read()
        elif section == "providers":
            provider, _, path = relative.partition("/")
            if provider in providers and (path == "provider.toml" or path.startswith("models/")):
                providers[provider][path] = archive.extractfile(member).read()

    rows = []
    for provider, files in providers.items():
        config = tomllib.loads(files["provider.toml"].decode())
        default_api = "responses" if config.get("opencodeai", "").endswith("/responses") else "chat-completions"
        models = {path.removeprefix("models/").removesuffix(".toml"): source
                  for path, source in files.items() if path.startswith("models/")}
        if provider == "openai":
            direct_labs = {key.removeprefix("openai/"): value for key, value in labs.items() if key.startswith("openai/")}
            for model in sorted(direct_labs.keys() - models.keys()):
                source = direct_labs[model]
                data = tomllib.loads(source.decode())
                rows.extend(project_model(provider, model, source, data, default_api, requires_builtin=True))
        for model, source in models.items():
            data = tomllib.loads(source.decode())
            base_model = data.get("base_model")
            if base_model:
                base = tomllib.loads(labs[base_model].decode())
                data = merge(base, data)
                for field in data.get("base_model_omit", []):
                    omit(data, field)
            rows.extend(project_model(provider, model, source, data, default_api, requires_builtin=False))
    rows.sort(key=lambda row: (row["source"], row["id"], row["api"]))
    return {"schema": 1, "commit": COMMIT, "archiveSha256": ARCHIVE_SHA256, "license": "MIT", "rows": rows}


def main():
    archive = Path(sys.argv[1])
    if hashlib.sha256(archive.read_bytes()).hexdigest() != ARCHIVE_SHA256:
        raise ValueError("models.dev archive differs from the pinned input")
    with tarfile.open(archive) as source:
        snapshot = project(source)
    print(json.dumps({key: value for key, value in snapshot.items() if key != "rows"}, sort_keys=True)[:-1] + ', "rows": [')
    print(",\n".join("  " + json.dumps(row, sort_keys=True) for row in snapshot["rows"]))
    print("]}")


if __name__ == "__main__":
    main()
