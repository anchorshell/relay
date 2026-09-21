import unittest
import asyncio
import json
import threading
import time
from types import SimpleNamespace
from worker import checkpoint_model_build
from worker import Application, Runtime, fit_edges, pack, questions, routing_questions, validate_request, TAXONOMY

class CheckpointLoadingTests(unittest.TestCase):
    def fixture(self):
        parameter = SimpleNamespace(shape=(2, 3), value="initial")
        original = lambda tensor, **kwargs: setattr(tensor, "value", "random")
        init = SimpleNamespace(trunc_normal_=original)
        model = SimpleNamespace(named_parameters=lambda: [("encoder.weight", parameter)], buffer=None)
        def build():
            init.trunc_normal_(parameter)
            model.buffer = "rotary initialized"
            return model
        return parameter, init, model, build, original

    def test_checkpoint_replaces_skipped_parameter_and_preserves_buffers(self):
        parameter, init, model, build, original = self.fixture()
        result = checkpoint_model_build(build, init, {"encoder.weight": (2, 3)})
        self.assertIs(result, model)
        self.assertEqual(parameter.value, "initial")
        self.assertEqual(model.buffer, "rotary initialized")
        self.assertIs(init.trunc_normal_, original)

    def test_missing_mismatched_or_unregistered_parameter_fails(self):
        for shapes in ({}, {"encoder.weight": (3, 2)}):
            _, init, _, build, original = self.fixture()
            with self.assertRaises(RuntimeError):
                checkpoint_model_build(build, init, shapes)
            self.assertIs(init.trunc_normal_, original)
        _, init, model, build, original = self.fixture()
        model.named_parameters = lambda: []
        with self.assertRaises(RuntimeError):
            checkpoint_model_build(build, init, {"encoder.weight": (2, 3)})
        self.assertIs(init.trunc_normal_, original)

    def test_initializer_restored_when_constructor_fails(self):
        _, init, _, _, original = self.fixture()
        def fail():
            raise ValueError("fixture")
        with self.assertRaises(ValueError):
            checkpoint_model_build(fail, init, {})
        self.assertIs(init.trunc_normal_, original)


class Tokenizer:
    mask_token = "[MASK]"
    def __call__(self, text, **kwargs):
        return {"input_ids": list(text)}

class PackingTests(unittest.TestCase):
    def test_head_tail_and_unicode(self):
        text = "Translate " + "日本語" * 3000 + " FINAL ASK"
        result = fit_edges(text, 100, list)
        self.assertTrue(result.startswith("Translate "))
        self.assertTrue(result.endswith("FINAL ASK"))
        self.assertLessEqual(len(result), 100)
        result.encode("utf-8", errors="strict")

    def test_rank_and_duplicate(self):
        items = [{"text": "Current task"}, {"text": "Current task"}, {"text": "Context"}]
        self.assertEqual(pack(items, 100, Tokenizer()), "Current task\nContext")
        self.assertEqual(pack(items, 100, Tokenizer()), pack(items, 100, Tokenizer()))

    def test_schema_rejects_raw_envelope_and_oversize(self):
        value = {"version": 1, "taxonomy_hash": TAXONOMY["hash"], "candidates": [{"text": "hello", "type": "current_task", "weight": 1}]}
        validate_request(value)
        value["messages"] = []
        with self.assertRaises(ValueError): validate_request(value)
        del value["messages"]
        value["candidates"][0]["text"] = "界" * 4096
        with self.assertRaises(ValueError): validate_request(value)

    def test_full_metadata_not_only_primary_action(self):
        schema = questions()
        self.assertEqual(len(schema), 2+len(TAXONOMY["actions"])+2*len(TAXONOMY["objects"])+len(TAXONOMY["domains"]))
        self.assertIn("needs_coder", schema)
        self.assertTrue(all("type" in question for question in schema.values()))

class RoutingTests(unittest.TestCase):
    def runtime(self):
        # No model load, Torch import, or real inference in these unit tests.
        runtime = Runtime.__new__(Runtime)
        runtime.budget = 512
        runtime.questions = questions()
        runtime.routing_questions = routing_questions()
        calls = []
        class Agent:
            tok = Tokenizer()
            def _to_internal(self, q): return q
            def predict(self, state, schema):
                calls.append((state, schema.copy()))
                probabilities = {a: float(a == "explain") for a in TAXONOMY["actions"]}
                return {"answers": {name: {"choice": "explain", "probabilities": probabilities}
                        if q["type"] == "choice" else {"noul": .1} for name, q in schema.items()}}
        runtime.agent = Agent()
        runtime.build_sequence = lambda tok, state, q, *bounds: (list(state), [])
        return runtime, calls

    def test_exactly_two_questions_in_one_predict(self):
        runtime, calls = self.runtime()
        result = runtime.classify_routing([{"text": "Explain queues."}], time.monotonic()+5, threading.Event())
        self.assertEqual(len(calls), 1)
        self.assertEqual(set(calls[0][1]), {"primary_action", "needs_coder"})
        self.assertEqual(calls[0][0], "Explain queues.")
        self.assertEqual(runtime.routing_questions["primary_action"], runtime.questions["primary"])
        self.assertEqual(runtime.routing_questions["needs_coder"], runtime.questions["needs_coder"])
        self.assertEqual(set(result), {"version", "taxonomy_hash", "model_version", "primary_action", "primary_confidence", "primary_probabilities", "needs_coder"})
        self.assertEqual(result["primary_action"], "explain")
        self.assertEqual(result["primary_confidence"], result["primary_probabilities"]["explain"])
        self.assertEqual(len(result["primary_probabilities"]), 29)

    def test_full_endpoint_retains_schema_and_microbatches(self):
        runtime, calls = self.runtime()
        result = runtime.classify([{"text": "Explain queues."}], time.monotonic()+5, threading.Event())
        self.assertEqual(len(runtime.questions), 166)
        self.assertEqual(len(calls), 42)
        self.assertEqual(sum(len(schema) for _, schema in calls), 166)
        self.assertTrue(all(state == "Explain queues." for state, _ in calls))
        self.assertEqual([len(result[k]) for k in ("actions", "objects", "domains")], [29, 110, 25])
        self.assertNotIn("primary_probabilities", result)

    def test_routing_cancelled_before_predict(self):
        runtime, calls = self.runtime()
        stopped = threading.Event()
        stopped.set()
        with self.assertRaises(TimeoutError):
            runtime.classify_routing([{"text": "Explain queues."}], time.monotonic()+5, stopped)
        self.assertEqual(calls, [])

class APITests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.app = Application()
        self.app.token = "fixture-secret"
        self.app.runtime = object()

    async def asyncTearDown(self):
        self.app.executor.shutdown(wait=True, cancel_futures=True)

    async def request(self, path, body=b"", authenticated=True):
        messages = []
        async def send(value): messages.append(value)
        delivered = False
        async def receive():
            nonlocal delivered
            if delivered: await asyncio.Event().wait()
            delivered = True
            return {"type": "http.request", "body": body, "more_body": False}
        scope = {"type": "http", "path": path, "method": "GET" if path.startswith("/health") else "POST", "headers": [(b"authorization", b"Bearer fixture-secret")] if authenticated else []}
        await self.app(scope, receive, send)
        return messages[0]["status"], json.loads(messages[1]["body"])

    async def test_readiness_requires_auth(self):
        status, _ = await self.request("/health/ready", authenticated=False)
        self.assertEqual(status, 401)
        status, value = await self.request("/health/ready")
        self.assertEqual((status, value), (200, {"ready": True}))

    async def test_invalid_and_oversized_requests_fail_before_inference(self):
        status, _ = await self.request("/classify", b'{"messages":[]}')
        self.assertEqual(status, 400)
        status, _ = await self.request("/classify", b"x" * 262145)
        self.assertEqual(status, 413)
        self.assertEqual(self.app.pending, 0)

    async def test_admission_is_bounded(self):
        self.app.pending = 8
        status, _ = await self.request("/classify", b"{}")
        self.assertEqual(status, 503)

    async def test_fixed_schema_reaches_one_runtime_call(self):
        calls = []
        class FakeRuntime:
            def classify(self, candidates, deadline, stopped):
                calls.append(candidates)
                return {"fixture": True}
        self.app.runtime = FakeRuntime()
        body = json.dumps({"version": 1, "taxonomy_hash": TAXONOMY["hash"], "candidates": [{"text": "Summarize this report.", "type": "current_task", "weight": 1}]}).encode()
        status, value = await self.request("/classify", body)
        self.assertEqual((status, value), (200, {"fixture": True}))
        self.assertEqual(len(calls), 1)
        self.assertEqual(self.app.pending, 0)

    async def test_routing_request_uses_same_bounds_auth_and_runtime(self):
        runtime, calls = RoutingTests().runtime()
        self.app.runtime = runtime
        body = json.dumps({"version": 1, "taxonomy_hash": TAXONOMY["hash"], "candidates": [{"text": "Explain queues.", "type": "current_task", "weight": 1}]}).encode()
        status, _ = await self.request("/classify-routing", body, authenticated=False)
        self.assertEqual(status, 401)
        status, _ = await self.request("/classify-routing", b'{"messages":[]}')
        self.assertEqual(status, 400)
        status, _ = await self.request("/classify-routing", b"x" * 262145)
        self.assertEqual(status, 413)
        self.app.pending = 8
        status, _ = await self.request("/classify-routing", body)
        self.assertEqual(status, 503)
        self.app.pending = 0
        self.assertEqual(calls, [])
        status, value = await self.request("/classify-routing", body)
        self.assertEqual(status, 200)
        self.assertEqual(value["primary_action"], "explain")
        self.assertEqual(len(calls), 1)
        self.assertEqual(self.app.pending, 0)

if __name__ == "__main__": unittest.main()
