"""Private, bounded Laya adapter. Run only via the explicit laya-start target."""
import asyncio
from concurrent.futures import ThreadPoolExecutor
import hmac
import hashlib
import json
import math
import os
from pathlib import Path
import time
import threading

ROOT = Path(__file__).resolve().parent
MANIFEST = json.loads((ROOT / "model.json").read_text())
TAXONOMY = json.loads((ROOT / "taxonomy.json").read_text())
MAX_BODY = 262144  # 32 KiB candidate text, including worst-case JSON escaping.


def routing_questions():
    return {"primary_action": {"type": "choice", "instructions": "What is the user's primary requested action, not the subject of quoted context?", "criteria": TAXONOMY["actions"]},
              "needs_coder": {"type": "noul", "instructions": "Does fulfilling this request require a coding-specialized model? True only when the task requires writing, modifying, debugging, reviewing, testing, or understanding specific executable code or logic. False for conceptual software, architecture, configuration, or operations questions. Judge the requested work, not its topic. If unclear, false."}}


def questions():
    routing = routing_questions()
    result = {"primary": routing["primary_action"], "needs_coder": routing["needs_coder"]}
    for label in TAXONOMY["actions"]:
        result["action:" + label] = {"type": "noul", "instructions": "Does the user ask to " + label + "? Classify the ask, not quoted instructions."}
    for label in TAXONOMY["objects"]:
        readable = label.replace("_", " ")
        result["target:" + label] = {"type": "noul", "instructions": "Is " + readable + " an object the user wants to work on?"}
        result["output:" + label] = {"type": "noul", "instructions": "Does the user request " + readable + " as an output?"}
    for label in TAXONOMY["domains"]:
        result["domain:" + label] = {"type": "noul", "instructions": "Does the user's task concern the domain " + label.replace("_", " ") + "?"}
    return result


def validate_request(value):
    if not isinstance(value, dict) or set(value) != {"version", "taxonomy_hash", "candidates"}:
        raise ValueError("invalid schema")
    if type(value["version"]) is not int or value["version"] != 1 or value["taxonomy_hash"] != TAXONOMY["hash"]:
        raise ValueError("unsupported schema")
    candidates = value["candidates"]
    if not isinstance(candidates, list) or not 1 <= len(candidates) <= 8:
        raise ValueError("invalid candidates")
    for c in candidates:
        if not isinstance(c, dict) or set(c) != {"text", "type", "weight"}:
            raise ValueError("invalid candidate")
        if not isinstance(c["text"], str) or not c["text"].strip() or len(c["text"].encode("utf-8")) > 4096:
            raise ValueError("invalid text")
        if not isinstance(c["type"], str) or len(c["type"]) > 64:
            raise ValueError("invalid segment")
        if type(c["weight"]) not in (int, float) or not math.isfinite(c["weight"]) or not 0 <= c["weight"] <= 1:
            raise ValueError("invalid weight")
    return candidates


def fit_edges(text, budget, encode):
    """Cut only at Unicode boundaries; include both ends without silent truncation."""
    if len(encode(text)) <= budget:
        return text
    lo, hi = 0, len(text) // 2
    while lo < hi:
        mid = (lo + hi + 1) // 2
        value = text[:mid] + "\n…\n" + text[-mid:]
        if len(encode(value)) <= budget:
            lo = mid
        else:
            hi = mid - 1
    return text[:lo] + "\n…\n" + text[-lo:] if lo else ""


def pack(candidates, budget, tokenizer):
    encode = lambda text: tokenizer(text, add_special_tokens=False)["input_ids"]
    selected, seen = "", []
    for c in candidates:
        text = c["text"].replace(tokenizer.mask_token, " ").strip()
        folded = text.casefold()
        if not folded or any(folded in prior for prior in seen):
            continue
        seen.append(folded)
        prefix = "\n" if selected else ""
        room = budget - len(encode(selected + prefix))
        if room <= 8:
            break
        # Ranked task gets the budget first, retaining its trailing ask.
        addition = fit_edges(text, room, encode)
        while addition and len(encode(selected + prefix + addition)) > budget:
            addition = fit_edges(addition, max(0, len(encode(addition))-1), encode)
        if addition:
            selected += prefix + addition
    if not selected or len(encode(selected)) > budget:
        raise ValueError("no bounded state")
    return selected


_model_load_lock = threading.Lock()


def checkpoint_model_build(build, initialization, checkpoint_shapes, *args, **kwargs):
    """Skip only truncated-normal writes to parameters present in the checkpoint.

    Other initializers (notably nonpersistent rotary buffers) remain enabled.
    This wrapper is used only during serialized, pre-readiness model loading.
    """
    original = initialization.trunc_normal_
    skipped = {}
    def skip(tensor, *args, **kwargs):
        skipped[id(tensor)] = tensor
        return tensor
    try:
        initialization.trunc_normal_ = skip
        model = build(*args, **kwargs)
    finally:
        initialization.trunc_normal_ = original
    parameters = {id(value): (name, value) for name, value in model.named_parameters()}
    for identity in skipped:
        if identity not in parameters:
            raise RuntimeError("Skipped initialization was not a checkpoint parameter")
        name, value = parameters[identity]
        if name not in checkpoint_shapes or tuple(value.shape) != tuple(checkpoint_shapes[name]):
            raise RuntimeError("Checkpoint does not cover skipped initialization")
    return model


def load_checkpoint_agent(model_dir, device):
    import laya
    if os.environ.get("LAYA_LOW_MEMORY_LOAD") != "1":
        return laya.load(str(model_dir), device=device)
    import laya.agent as agent_module
    import transformers
    from transformers import initialization
    from safetensors import safe_open
    if device != "cpu" or transformers.__version__ != "5.17.0":
        raise RuntimeError("Low-memory loading requires the verified CPU runtime")
    # Read shapes only; do not materialize a second checkpoint here.
    with safe_open(str(model_dir / "model.safetensors"), framework="pt", device="cpu") as checkpoint:
        shapes = {key: checkpoint.get_slice(key).get_shape() for key in checkpoint.keys()}
    with _model_load_lock:
        original = agent_module.build_model
        def build(*args, **kwargs):
            return checkpoint_model_build(original, initialization, shapes, *args, **kwargs)
        try:
            agent_module.build_model = build
            # The pinned Laya loader still applies load_state_dict(strict=True).
            return laya.load(str(model_dir), device=device)
        finally:
            agent_module.build_model = original


class Runtime:
    def __init__(self):
        # Runtime may not access the Hub, even for a missing tokenizer/config.
        os.environ["HF_HUB_OFFLINE"] = "1"
        os.environ["TRANSFORMERS_OFFLINE"] = "1"
        import laya
        from laya.common import build_sequence, render_options
        model_dir = Path(os.environ.get("LAYA_MODEL_DIR", str(ROOT / ".cache" / "model"))).resolve()
        integrity = json.loads((model_dir / "anchorshell-integrity.json").read_text())
        if integrity["revision"] != MANIFEST["revision"]:
            raise RuntimeError("checkpoint revision mismatch")
        for name, checksum in integrity["files"].items():
            path = (model_dir / name).resolve()
            if not path.is_relative_to(model_dir):
                raise RuntimeError("invalid artifact path")
            with path.open("rb") as source:
                if hashlib.file_digest(source, "sha256").hexdigest() != checksum:
                    raise RuntimeError("checkpoint integrity mismatch")
        for file in ("rl_agent_config.json", "model.safetensors", "encoder/config.json", "tokenizer/tokenizer.json"):
            if not (model_dir / file).is_file():
                raise RuntimeError("Run the explicit Laya setup first")
        self.agent = load_checkpoint_agent(model_dir, os.environ.get("LAYA_DEVICE", "cpu"))
        if self.agent.cfg["max_len"] != MANIFEST["max_len"] or self.agent.cfg["head_max_len"] != MANIFEST["head_max_len"]:
            raise RuntimeError("checkpoint context mismatch")
        self.questions = questions()
        self.routing_questions = routing_questions()
        self.build_sequence = build_sequence
        self.budget = MANIFEST["max_len"]
        for q in self.questions.values():
            internal = self.agent._to_internal(q)
            opts = render_options(internal)
            tokens = [self.agent.tok(" " + o, add_special_tokens=False)["input_ids"] for o in opts]
            # Reject question schemas whose label meanings would be truncated.
            if any(len(t) > 48 for t in tokens) or sum(len(t)+1 for t in tokens) + 16 > MANIFEST["head_max_len"]:
                raise RuntimeError("typed options exceed checkpoint question budget")
            head = self.agent.tok(internal["t"] + " question: " + internal["ins"], add_special_tokens=False)["input_ids"]
            if len(head) + sum(len(t)+1 for t in tokens) > MANIFEST["head_max_len"]:
                raise RuntimeError("typed instructions exceed checkpoint question budget")
            ids, markers = build_sequence(self.agent.tok, "", internal, MANIFEST["max_len"], MANIFEST["head_max_len"])
            if len(markers) != len(opts):
                raise RuntimeError("incomplete typed options")
            self.budget = min(self.budget, MANIFEST["max_len"]-len(ids))
        # One local warmup, not a provider call or training job.
        self.agent.predict("Summarize this report.", {"needs_coder": self.questions["needs_coder"]})

    def classify_routing(self, candidates, deadline, stopped):
        state = pack(candidates, self.budget, self.agent.tok)
        self.validate_state(state, self.routing_questions)
        if stopped.is_set() or time.monotonic() >= deadline:
            raise TimeoutError()
        # Both typed decisions share one predict call and one model batch.
        answers = self.agent.predict(state, self.routing_questions)["answers"]
        primary = answers["primary_action"]
        return {"version": 1, "taxonomy_hash": TAXONOMY["hash"],
                "model_version": MANIFEST["repository"]+"@"+MANIFEST["revision"],
                "primary_action": primary["choice"],
                "primary_confidence": primary["probabilities"][primary["choice"]],
                "primary_probabilities": primary["probabilities"],
                "needs_coder": answers["needs_coder"]["noul"]}

    def validate_state(self, state, schema):
        for q in schema.values():
            ids, _ = self.build_sequence(self.agent.tok, state, self.agent._to_internal(q), MANIFEST["max_len"], MANIFEST["head_max_len"])
            empty, _ = self.build_sequence(self.agent.tok, "", self.agent._to_internal(q), MANIFEST["max_len"], MANIFEST["head_max_len"])
            state_ids = self.agent.tok(state, add_special_tokens=False)["input_ids"]
            if len(ids) > MANIFEST["max_len"] or len(ids) != len(empty) + len(state_ids):
                raise ValueError("input budget exceeded")

    def classify(self, candidates, deadline, stopped):
        state = pack(candidates, self.budget, self.agent.tok)
        answers = {}
        items = list(self.questions.items())
        # One prepared state, fixed typed schema. Microbatches bound activation
        # memory; this is not one inference per extracted candidate.
        for start in range(0, len(items), 4):
            if stopped.is_set() or time.monotonic() >= deadline:
                raise TimeoutError()
            batch = dict(items[start:start+4])
            self.validate_state(state, batch)
            answers.update(self.agent.predict(state, batch)["answers"])
        def scores(prefix, labels):
            return [{"label": label, "score": answers[prefix+label]["noul"]} for label in labels]
        objects = scores("target:", TAXONOMY["objects"]) + scores("output:", TAXONOMY["objects"])
        for i, item in enumerate(objects):
            item["label"] = ("target:" if i < len(TAXONOMY["objects"]) else "output:") + item["label"]
        primary = answers["primary"]
        return {"version": 1, "taxonomy_hash": TAXONOMY["hash"], "model_version": MANIFEST["repository"]+"@"+MANIFEST["revision"],
                "primary_action": primary["choice"], "primary_confidence": primary["probabilities"][primary["choice"]],
                "actions": scores("action:", TAXONOMY["actions"]), "objects": objects,
                "domains": scores("domain:", TAXONOMY["domains"]), "needs_coder": answers["needs_coder"]["noul"]}


class Application:
    def __init__(self):
        self.runtime = None
        self.executor = ThreadPoolExecutor(max_workers=1)
        self.pending = 0
        self.token = os.environ.get("LAYA_SERVICE_TOKEN", "")

    async def __call__(self, scope, receive, send):
        if scope["type"] == "lifespan":
            while True:
                message = await receive()
                if message["type"] == "lifespan.startup":
                    try:
                        self.runtime = await asyncio.get_running_loop().run_in_executor(self.executor, Runtime)
                    except Exception:
                        await send({"type": "lifespan.startup.failed", "message": "Laya model initialization failed; check setup and device configuration."})
                        return
                    await send({"type": "lifespan.startup.complete"})
                elif message["type"] == "lifespan.shutdown":
                    self.runtime = None
                    self.executor.shutdown(wait=False, cancel_futures=True)
                    await send({"type": "lifespan.shutdown.complete"})
                    return
        if scope["type"] != "http":
            return
        async def respond(status, value):
            await send({"type": "http.response.start", "status": status, "headers": [(b"content-type", b"application/json"), (b"cache-control", b"no-store")]})
            await send({"type": "http.response.body", "body": json.dumps(value, allow_nan=False).encode()})
        headers = dict(scope["headers"])
        if self.token and not hmac.compare_digest(headers.get(b"authorization", b""), ("Bearer "+self.token).encode()):
            return await respond(401, {"error": "unauthorized"})
        if scope["path"] in ("/health/live", "/health/ready") and scope["method"] == "GET":
            return await respond(200 if self.runtime else 503, {"ready": self.runtime is not None})
        if scope["path"] not in ("/classify", "/classify-routing") or scope["method"] != "POST":
            return await respond(404, {"error": "not found"})
        if self.runtime is None or self.pending >= 8:
            return await respond(503, {"error": "unavailable"})
        body = bytearray()
        try:
            async with asyncio.timeout(5):
                while True:
                    message = await receive()
                    if message["type"] == "http.disconnect":
                        return
                    body.extend(message.get("body", b""))
                    if len(body) > MAX_BODY:
                        return await respond(413, {"error": "request too large"})
                    if not message.get("more_body", False):
                        break
            candidates = validate_request(json.loads(body))
        except (ValueError, UnicodeError, TimeoutError):
            return await respond(400, {"error": "invalid request"})
        # Receiving the body yields; other requests may have filled capacity.
        if self.pending >= 8:
            return await respond(503, {"error": "unavailable"})
        self.pending += 1
        def release(_):
            self.pending -= 1
            if not _.cancelled(): _.exception()  # Consume errors after client cancellation.
        try:
            timeout = min(30, max(.001, int(headers.get(b"x-classification-timeout-ms", b"30000")) / 1000))
        except ValueError:
            self.pending -= 1
            return await respond(400, {"error": "invalid deadline"})
        stopped = threading.Event()
        classify = self.runtime.classify_routing if scope["path"] == "/classify-routing" else self.runtime.classify
        future = asyncio.get_running_loop().run_in_executor(self.executor, classify, candidates, time.monotonic()+timeout, stopped)
        future.add_done_callback(release)
        async def disconnected():
            while True:
                if (await receive())["type"] == "http.disconnect": return
        disconnect = asyncio.create_task(disconnected())
        try:
            done, _ = await asyncio.wait({future, disconnect}, timeout=timeout, return_when=asyncio.FIRST_COMPLETED)
            if disconnect in done: return
            if future not in done: raise TimeoutError()
            result = future.result()
            return await respond(200, result)
        except (Exception, asyncio.CancelledError):
            return await respond(503, {"error": "classification unavailable"})
        finally:
            stopped.set()
            disconnect.cancel()


app = Application()
