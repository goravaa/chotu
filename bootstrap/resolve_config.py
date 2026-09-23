#!/usr/bin/env python3
import os
import re
import shlex
import sys


DEFAULTS = {
    "persistence": True,
    "inference.runtime": "vllm",
    "inference.model": "cyankiwi/Qwen3-Coder-30B-A3B-Instruct-AWQ-4bit",
    "inference.quantization": "awq",
    "inference.access": "local",
    "inference.image": "vllm/vllm-openai:latest",
    "inference.port": 8000,
    "inference.resources.max_model_len": 14336,
    "inference.resources.gpu_memory_utilization": 0.97,
    "inference.resources.output_limit": 4096,
    "tooling.packages": [],
    "sync.bucket": "",
    "sync.prefix": "workspace",
    "sync.interval": "1m",
    "sync.exclusions": [],
    "status_page.port": 8080,
}


def parse_scalar(raw):
    raw = raw.strip()
    if raw == "":
        return ""
    if raw in ("true", "True", "yes", "Yes", "on", "On"):
        return True
    if raw in ("false", "False", "no", "No", "off", "Off"):
        return False
    if raw in ("null", "Null", "~"):
        return None
    if (raw.startswith('"') and raw.endswith('"')) or (
        raw.startswith("'") and raw.endswith("'")
    ):
        return raw[1:-1]
    if raw.startswith("[") and raw.endswith("]"):
        inner = raw[1:-1].strip()
        if not inner:
            return []
        return [parse_scalar(part.strip()) for part in inner.split(",")]
    if re.fullmatch(r"-?\d+", raw):
        return int(raw)
    if re.fullmatch(r"-?\d+\.\d+", raw):
        return float(raw)
    return raw


def strip_comment(line):
    in_single = False
    in_double = False
    for i, ch in enumerate(line):
        if ch == "'" and not in_double:
            in_single = not in_single
        elif ch == '"' and not in_single:
            in_double = not in_double
        elif ch == "#" and not in_single and not in_double:
            return line[:i]
    return line


def parse_simple_yaml(path):
    root = {}
    stack = [(-1, root)]
    last_empty = None

    with open(path, "r", encoding="utf-8") as fh:
        for lineno, original in enumerate(fh, 1):
            line = strip_comment(original.rstrip("\n"))
            if not line.strip():
                continue
            indent = len(line) - len(line.lstrip(" "))
            text = line.strip()
            if "\t" in line[:indent]:
                raise ValueError(f"line {lineno}: tabs are not supported in indentation")

            while stack and indent <= stack[-1][0]:
                stack.pop()
            parent = stack[-1][1]

            if text.startswith("- "):
                if last_empty and indent > last_empty[0]:
                    owner, key, empty_indent = last_empty[1], last_empty[2], last_empty[0]
                    if owner.get(key) == {}:
                        owner[key] = []
                        while stack and stack[-1][0] > empty_indent:
                            stack.pop()
                        stack.append((empty_indent, owner[key]))
                        parent = owner[key]
                if not isinstance(parent, list):
                    raise ValueError(f"line {lineno}: list item without list parent")
                parent.append(parse_scalar(text[2:]))
                continue

            if ":" not in text:
                raise ValueError(f"line {lineno}: expected key: value")

            key, raw_value = text.split(":", 1)
            key = key.strip()
            raw_value = raw_value.strip()
            if raw_value == "":
                parent[key] = {}
                last_empty = (indent, parent, key)
                stack.append((indent, parent[key]))
            else:
                parent[key] = parse_scalar(raw_value)
                last_empty = None

    return root


def get(data, dotted, default=None):
    node = data
    for part in dotted.split("."):
        if not isinstance(node, dict) or part not in node:
            return default
        node = node[part]
    return node


def bool_value(value, name):
    if isinstance(value, bool):
        return value
    raise ValueError(f"{name} must be true or false")


def string_value(value, name, allow_empty=False):
    if isinstance(value, str) and (allow_empty or value.strip()):
        return value.strip()
    raise ValueError(f"{name} must be a string")


def list_value(value, name):
    if value is None:
        return []
    if not isinstance(value, list) or not all(isinstance(v, str) and v.strip() for v in value):
        raise ValueError(f"{name} must be a list of strings")
    return [v.strip() for v in value]


def load_config(path):
    try:
        return parse_simple_yaml(path)
    except Exception as exc:
        raise SystemExit(f"Invalid config YAML in {path}: {exc}") from exc


def resolve(config_path):
    data = load_config(config_path)
    cfg = {key: get(data, key, default) for key, default in DEFAULTS.items()}

    persistence = bool_value(cfg["persistence"], "persistence")
    runtime = string_value(cfg["inference.runtime"], "inference.runtime")
    model = string_value(cfg["inference.model"], "inference.model")
    quantization = string_value(cfg["inference.quantization"], "inference.quantization")
    access = string_value(cfg["inference.access"], "inference.access")
    image = string_value(cfg["inference.image"], "inference.image")
    bucket = string_value(cfg["sync.bucket"], "sync.bucket", allow_empty=True)
    prefix = string_value(cfg["sync.prefix"], "sync.prefix", allow_empty=True).strip("/")
    interval = string_value(cfg["sync.interval"], "sync.interval")
    packages = list_value(cfg["tooling.packages"], "tooling.packages")
    exclusions = list_value(cfg["sync.exclusions"], "sync.exclusions")

    if runtime != "vllm":
        raise SystemExit("Unsupported inference.runtime: only 'vllm' is supported")
    if quantization != "awq":
        raise SystemExit("Unsupported inference.quantization: only 'awq' is supported")
    if access != "local":
        raise SystemExit("Unsupported inference.access: only 'local' is supported")

    try:
        port = int(cfg["inference.port"])
        status_port = int(cfg["status_page.port"])
        max_model_len = int(cfg["inference.resources.max_model_len"])
        output_limit = int(cfg["inference.resources.output_limit"])
        gpu_memory_utilization = float(cfg["inference.resources.gpu_memory_utilization"])
    except Exception as exc:
        raise SystemExit(f"Invalid numeric config value: {exc}") from exc

    if not 1 <= port <= 65535:
        raise SystemExit("inference.port must be between 1 and 65535")
    if not 1 <= status_port <= 65535:
        raise SystemExit("status_page.port must be between 1 and 65535")
    if max_model_len < 1024:
        raise SystemExit("inference.resources.max_model_len must be at least 1024")
    if output_limit < 1:
        raise SystemExit("inference.resources.output_limit must be positive")
    if not 0.1 <= gpu_memory_utilization <= 1.0:
        raise SystemExit("inference.resources.gpu_memory_utilization must be between 0.1 and 1.0")
    if not re.fullmatch(r"\d+[smhd]", interval):
        raise SystemExit("sync.interval must look like 1m, 30s, 2h, or 1d")

    run_user = os.environ.get("SUDO_USER") or os.environ.get("USER") or "ubuntu"
    home = os.path.expanduser(f"~{run_user}")
    if home == f"~{run_user}":
        home = f"/home/{run_user}"

    if persistence:
        storage_mode = "persistent"
        storage_root = "/"
        docker_root = "/var/lib/docker"
        model_cache = "/var/lib/chotu/huggingface"
        state_dir = "/var/lib/chotu"
        workspace = f"{home}/workspace"
    else:
        storage_mode = "ephemeral"
        storage_root = "/data"
        docker_root = "/data/docker"
        model_cache = "/data/huggingface"
        state_dir = "/data/chotu"
        workspace = "/data/workspace"

    return {
        "CHOTU_BOOTSTRAP_VERSION": "0.1.0",
        "CHOTU_CONFIG_FILE": config_path,
        "CHOTU_RUN_USER": run_user,
        "CHOTU_RUN_HOME": home,
        "CHOTU_PERSISTENCE": "true" if persistence else "false",
        "CHOTU_STORAGE_MODE": storage_mode,
        "CHOTU_STORAGE_ROOT": storage_root,
        "CHOTU_DOCKER_ROOT": docker_root,
        "CHOTU_MODEL_CACHE": model_cache,
        "CHOTU_STATE": state_dir,
        "CHOTU_WORKSPACE": workspace,
        "CHOTU_RUNTIME": runtime,
        "CHOTU_MODEL": model,
        "CHOTU_QUANTIZATION": quantization,
        "CHOTU_INFERENCE_IMAGE": image,
        "CHOTU_INFERENCE_PORT": str(port),
        "CHOTU_MAX_MODEL_LEN": str(max_model_len),
        "CHOTU_GPU_MEMORY_UTILIZATION": str(gpu_memory_utilization),
        "CHOTU_OUTPUT_LIMIT": str(output_limit),
        "CHOTU_S3_BUCKET": bucket,
        "CHOTU_S3_PREFIX": prefix,
        "CHOTU_SYNC_INTERVAL": interval,
        "CHOTU_STATUS_PORT": str(status_port),
        "CHOTU_TOOLING_PACKAGES": " ".join(shlex.quote(p) for p in packages),
        "CHOTU_SYNC_EXCLUSIONS": "\n".join(exclusions),
    }


def write_env(env, out_path):
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as out:
        out.write("# Generated by chotu bootstrap. Do not edit by hand.\n")
        for key in sorted(env):
            out.write(f"export {key}={shlex.quote(env[key])}\n")


def main():
    if len(sys.argv) != 3:
        raise SystemExit("usage: resolve_config.py CONFIG_PATH OUTPUT_PATH")

    config_path, out_path = sys.argv[1], sys.argv[2]
    write_env(resolve(config_path), out_path)


if __name__ == "__main__":
    main()
