#!/usr/bin/env python3
"""Render the LiteLLM proxy config from env at container start.

CHAT is an ordered overflow/failover CHAIN. Layers L1..N are each their own
LiteLLM model group, wired with ordered `fallbacks` (L1 -> L2 -> ... -> LN) and a
per-layer concurrency / rpm CAP. A request hits L1; when L1 is erroring/cooled-down LiteLLM
fails over to L2, then L3, and so on. tela calls the chain by L1's model name, so
set TELA_LLM_MODEL = TELA_LLM_1_MODEL.

OVERLOAD (vs failure) is handled in the BACKEND, not here: tela's foreground
ask/assist gate caps concurrency on L1 and, when saturated, calls a stable
`tela-chat-overflow` alias this config registers (→ L2, then L3..). So "L1 down"
spills via LiteLLM fallbacks; "L1 overloaded" spills via the backend calling the
overflow alias. Both land on L2. The alias is registered automatically whenever
≥2 layers exist; point the backend at it with TELA_LLM_OVERFLOW_MODEL.

  Per layer i (1-based, contiguous — enumeration stops at the first missing _URL):
    TELA_LLM_{i}_URL           OpenAI-compatible base (an mlx/Ollama /v1, a wrapper, a cloud)
    TELA_LLM_{i}_MODEL         model name sent upstream
    TELA_LLM_{i}_KEY           bearer/key ("none" for an open local endpoint)
    TELA_LLM_{i}_PROVIDER      litellm provider prefix (default "openai")
    TELA_LLM_{i}_MAX_PARALLEL  max concurrent requests before spilling (0 = unlimited)
    TELA_LLM_{i}_RPM           requests/min cap before spilling (0 = unlimited)

EMBED is single — a relief embedder would have to be the SAME 1024-d model on a
second host (different-dim vectors aren't interchangeable in page_chunks), so
embed failover is out of scope here:
    TELA_EMBED_URL / _MODEL / _KEY / _PROVIDER   (TELA_EMBED_PRIMARY_* also accepted)

Global:
    LITELLM_MASTER_KEY    required; tela presents it as its bearer.
    TELA_AI_NUM_RETRIES   (default 1)  same-layer retries before the next layer
    TELA_AI_ALLOWED_FAILS (default 2)  failures before a layer is cooled down
    TELA_AI_COOLDOWN      (default 30) seconds a cooled-down layer sits out
    TELA_AI_TIMEOUT       (default 150) per-request timeout (seconds)

Output is JSON (valid YAML) — no yaml dep, no quoting pitfalls.
"""
import json
import os


def env(*names, default=""):
    for n in names:
        v = os.getenv(n)
        if v is not None and v.strip() != "":
            return v.strip()
    return default


def envint(name, default=0):
    try:
        return int(env(name, default=str(default)))
    except ValueError:
        return default


MASTER_KEY = env("LITELLM_MASTER_KEY")
if not MASTER_KEY:
    raise SystemExit("LITELLM_MASTER_KEY is required")

NUM_RETRIES = envint("TELA_AI_NUM_RETRIES", 1)
ALLOWED_FAILS = envint("TELA_AI_ALLOWED_FAILS", 2)
COOLDOWN = envint("TELA_AI_COOLDOWN", 30)
TIMEOUT = envint("TELA_AI_TIMEOUT", 150)


def deployment(model_name, url, model, key, provider, max_parallel=0, rpm=0):
    # provider is the part before the first "/", so a model name that itself
    # contains slashes (e.g. mlx-community/Qwen3-…) still resolves correctly.
    params = {"model": f"{provider}/{model}", "api_base": url, "api_key": key or "none"}
    if max_parallel > 0:
        params["max_parallel_requests"] = max_parallel
    if rpm > 0:
        params["rpm"] = rpm
    return {"model_name": model_name, "litellm_params": params}


model_list = []
fallbacks = []

# --- chat: ordered chain L1..N ---
layers = []
i = 1
while env(f"TELA_LLM_{i}_URL"):
    layers.append({
        "url": env(f"TELA_LLM_{i}_URL"),
        "model": env(f"TELA_LLM_{i}_MODEL"),
        "key": env(f"TELA_LLM_{i}_KEY", default="none"),
        "provider": env(f"TELA_LLM_{i}_PROVIDER", default="openai"),
        "max_parallel": envint(f"TELA_LLM_{i}_MAX_PARALLEL", 0),
        "rpm": envint(f"TELA_LLM_{i}_RPM", 0),
    })
    i += 1

if layers:
    base = layers[0]["model"]  # tela sends this (TELA_LLM_MODEL)
    names = []
    for idx, L in enumerate(layers):
        name = base if idx == 0 else f"{base}-l{idx + 1}"
        names.append(name)
        model_list.append(deployment(name, L["url"], L["model"], L["key"],
                                     L["provider"], L["max_parallel"], L["rpm"]))
    if len(names) > 1:
        fallbacks.append({base: names[1:]})  # ordered failover chain (L1 down -> L2 -> ..)

    # Stable overload-spill alias the backend calls when its foreground gate is
    # full (L1 healthy but saturated). Targets L2 and fails over down the rest of
    # the chain (L3..). Its name is fixed (independent of L1's model name), so the
    # backend's TELA_LLM_OVERFLOW_MODEL doesn't change when L1's model does.
    if len(layers) > 1:
        l2 = layers[1]
        model_list.append(deployment("tela-chat-overflow", l2["url"], l2["model"],
                                      l2["key"], l2["provider"]))
        if len(names) > 2:
            fallbacks.append({"tela-chat-overflow": names[2:]})

# --- embed: single ---
embed_url = env("TELA_EMBED_URL", "TELA_EMBED_PRIMARY_URL")
if embed_url:
    em = env("TELA_EMBED_MODEL", "TELA_EMBED_PRIMARY_MODEL")
    model_list.append(deployment(
        em, embed_url, em,
        env("TELA_EMBED_KEY", "TELA_EMBED_PRIMARY_KEY", default="none"),
        env("TELA_EMBED_PROVIDER", "TELA_EMBED_PRIMARY_PROVIDER", default="openai"),
    ))

if not model_list:
    raise SystemExit("no upstreams configured — set TELA_LLM_1_URL / TELA_EMBED_URL")

router_settings = {
    "num_retries": NUM_RETRIES,
    "timeout": TIMEOUT,
    "allowed_fails": ALLOWED_FAILS,
    "cooldown_time": COOLDOWN,
}
if fallbacks:
    router_settings["fallbacks"] = fallbacks

config = {
    "model_list": model_list,
    "router_settings": router_settings,
    "litellm_settings": {
        "callbacks": ["prometheus"],
        "drop_params": True,
        "request_timeout": TIMEOUT,
    },
    "general_settings": {"master_key": MASTER_KEY},
}

print(json.dumps(config, indent=2))
