/** @format */

import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";
import { DEFAULT_RELAY_ORIGIN } from "../../web/utils/relayDefaults.ts";

const root = new URL("../../", import.meta.url);
const read = (path) => readFileSync(new URL(path, root), "utf8");
const readme = read("README.md");
const visible = readme.replace(/<!--[\s\S]*?-->/g, "");

test("README stays compact and uses current branding, credentials, and local ports", () => {
  assert.ok(readme.split("\n").length <= 300);
  const copy = readme.replaceAll(
    "https://github.com/anchorshell/relay",
    "REPOSITORY",
  );
  assert.doesNotMatch(
    copy,
    /bouncer|BOUNCER_|808[01]|X-Relay-|open[ -]source|\bOSS\b/i,
  );
  assert.match(
    visible,
    /AI gateway built for agents — and the people behind them/,
  );
  assert.match(visible, /relay\.db/);
  for (const key of [
    "RELAY_API_TOKEN",
    "RELAY_ADMIN_TOKEN",
    "RELAY_MASTER_KEY",
  ])
    assert.ok(visible.includes(key));
  assert.ok(visible.includes(DEFAULT_RELAY_ORIGIN));
  assert.match(visible, /localhost:3030/);
  assert.match(visible, /ws:\/\/localhost:11730\/api\/ws/);
});

test("README relative links and logo assets exist; no nonexistent visual is embedded", () => {
  const targets = [
    ...[...visible.matchAll(/(?:href|src|srcset)="([^"]+)"/g)].map(
      (match) => match[1],
    ),
    ...[...visible.matchAll(/\]\(([^)]+)\)/g)].map((match) => match[1]),
  ];
  for (const target of targets) {
    if (/^(?:https?:|#)/.test(target)) continue;
    assert.ok(existsSync(new URL(target.split("#")[0], root)), target);
  }
  assert.equal((visible.match(/img\.shields\.io/g) || []).length, 4);
  assert.doesNotMatch(
    visible,
    /docs\/assets\/|discord\.gg|github\.com\/.*\/actions\/|\/discussions|\/graphs\/contributors/,
  );
  assert.match(
    visible,
    /<img src="web\/public\/landing-gif\.gif" width="100%" alt="AnchorShell Relay in action"/,
  );
  assert.doesNotMatch(
    readme,
    /Replace the preview notice|A Realtime dashboard screenshot is coming/,
  );
  const preview = readFileSync(new URL("web/public/landing-gif.gif", root));
  assert.match(preview.subarray(0, 6).toString("ascii"), /^GIF8[79]a$/);
});

test("README quick start uses supported source-build targets, not an invented package", () => {
  assert.match(
    visible,
    /git clone https:\/\/github\.com\/anchorshell\/bouncer\.git anchorshell-relay\ncd anchorshell-relay\nmake deps\nmake install-dev-tools\nmake dev/,
  );
  assert.doesNotMatch(visible, /\bnpx\b/);
  const makefile = read("Makefile");
  for (const [, target] of visible.matchAll(/^make ([a-z-]+)$/gm)) {
    assert.match(makefile, new RegExp(`^${target}:`, "m"), target);
  }
  assert.match(makefile, /^dev:$/m);
  assert.match(makefile, /^FRONTEND_PORT \?= 3030$/m);
  assert.match(
    visible,
    /Open \*\*\[localhost:3030\]\(http:\/\/localhost:3030\)\*\* for the dashboard/,
  );
  assert.doesNotMatch(visible, /Open \*\*\[localhost:11730|\bmake run\b/);
  assert.match(visible, /\*\*Backend API:\*\* `http:\/\/localhost:11730`/);
  assert.match(
    visible,
    new RegExp(read(".nvmrc").trim().replaceAll(".", "\\.")),
  );
  assert.match(
    visible,
    new RegExp(/^go (.+)$/m.exec(read("go.mod"))[1].replaceAll(".", "\\.")),
  );
});

test("README first request is one authenticated curl using the documented route and body", () => {
  const curls = [...visible.matchAll(/```bash\n(curl [\s\S]*?)\n```/g)];
  assert.equal(curls.length, 1);
  const command = curls[0][1];
  assert.ok(
    command.startsWith(
      `curl -X POST ${DEFAULT_RELAY_ORIGIN}/v1/chat/completions`,
    ),
  );
  const headers = [...command.matchAll(/-H '([^']+)'/g)].map(
    (match) => match[1],
  );
  assert.deepEqual(headers, [
    "Authorization: Bearer <RELAY_API_TOKEN>",
    "Content-Type: application/json",
  ]);
  assert.deepEqual(JSON.parse(/-d '([\s\S]+)'/.exec(command)[1]), {
    model: "<MODEL_OR_GROUP>",
    messages: [{ role: "user", content: "Hello from Relay" }],
  });
  assert.doesNotMatch(command, /\$|\bjq\b|[|;]/);
  const contract = JSON.parse(read("docs/openapi.json"));
  assert.ok(contract.paths["/chat/completions"].post);
  assert.match(visible, /In \*\*Providers\*\*, add/);
});

test("README explains embedded packaging and keeps provider-specific details in documentation", () => {
  assert.match(visible, /Fast\. Flexible\. Intuitive\./);
  assert.doesNotMatch(
    visible,
    /\bfastest\b|most intuitive|OpenAI \(or native\)/i,
  );
  assert.match(visible, /Go backend\. Nuxt frontend\./);
  assert.match(visible, /one application binary/);
  assert.match(read("internal/ui/embed.go"), /\/\/go:embed all:dist/);
  assert.match(read("Makefile"), /^build: web-build embed-ui$/m);
  assert.match(visible, /single Go application with the dashboard built in/);
  assert.doesNotMatch(
    visible,
    /provider-compatibility|SigV4|refresh Google access tokens|native Anthropic Messages|round-robin/,
  );
  assert.ok(visible.includes("(docs/PROVIDER_COMPATIBILITY.md)"));
  const compatibility = read("docs/PROVIDER_COMPATIBILITY.md");
  assert.match(compatibility, /Relay does not perform AWS SigV4 signing/);
  assert.match(compatibility, /Relay does not refresh Google access tokens/);
  assert.match(
    compatibility,
    /it does not translate native Anthropic Messages, Bedrock Converse, or Vertex/,
  );
  assert.match(compatibility, /not round-robin or weighted load balancing/);
});

test("README shows the request flow and links to provider, group, and routing documentation", () => {
  const flow = /## How it works\n([\s\S]*?)\n## Self-hosted vs hosted/.exec(
    visible,
  )[1];
  assert.match(
    flow,
    /Your app talks to Relay through one OpenAI-compatible API/,
  );
  assert.match(
    flow,
    /Your application[\s\S]*Route[\s\S]*Queue \+ Pace[\s\S]*Guard[\s\S]*Track[\s\S]*OpenAI\s+Anthropic\s+Local models/,
  );
  assert.match(flow, /You choose the target\. Relay handles the traffic\./);
  assert.match(
    flow,
    /Provider credentials stay in Relay instead of being copied into every application/,
  );
  for (const path of [
    "docs/providers",
    "docs/groups",
    "guides/tune-wait-pacing-cooldowns",
  ]) {
    assert.ok(flow.includes(`https://anchorshell.com/${path}`), path);
  }
  assert.match(
    flow,
    /Relay connects to OpenAI-compatible cloud and local model endpoints/,
  );
});

test("README distinguishes Community features, hosted tiers, and the actual license", () => {
  const features = /## Features\n([\s\S]*?)\n## How it works/.exec(visible)[1];
  assert.equal((features.match(/^- /gm) || []).length, 11);
  assert.match(
    features.trimStart(),
    /^- \*\*Agentic-first\*\* — route, pace, guard, and observe model calls from agents, workflows, and applications through the same infrastructure\./,
  );
  const communityBullets = features
    .split("\n")
    .filter((line) => line.startsWith("- "))
    .join("\n");
  assert.doesNotMatch(
    communityBullets,
    /Smart Groups|Teams|per-user|per-key|SSO|subscription accounts/i,
  );
  assert.match(communityBullets, /Automatic request categorization/);
  assert.match(communityBullets, /Guardrails against data leaks/);
  assert.match(
    visible,
    /Hosted Relay adds stronger request classification and Smart Groups/,
  );
  const introduction = /## What is Relay\?\n([\s\S]*?)\n## Quick start/.exec(
    visible,
  )[1];
  assert.match(
    introduction,
    /Stop hard-coding your app to one model\. One API\. Any model\. Smarter routing\. Built for agents from day one\./,
  );
  assert.match(introduction, /based on intent, domain, complexity/);
  assert.match(introduction, /\[Get started\]\(#quick-start\)/);
  assert.match(
    introduction,
    /\[Read the docs\]\(https:\/\/anchorshell\.com\/docs\)/,
  );
  assert.match(
    introduction,
    /\[Try hosted Relay\]\(https:\/\/anchorshell\.com\/\)/,
  );
  assert.doesNotMatch(introduction, /chatgpt\.com/);
  assert.doesNotMatch(
    introduction,
    /Hugging Face|huggingface\.co|OpenAI-compatible/,
  );
  assert.match(visible, /Hosted Relay includes a free tier/);
  assert.match(visible, /Teams and invitations \| — \| Team \/ Enterprise/);
  const licenseName = read("LICENSE").split("\n")[0];
  assert.ok(visible.includes(`[${licenseName}](LICENSE)`));
  assert.match(visible, /source available/);
  assert.match(
    visible,
    /qualifying small businesses, subject to\nthe license terms/,
  );
});
