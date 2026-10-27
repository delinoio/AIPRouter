// SPDX-License-Identifier: Apache-2.0
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  existsSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { setTimeout as delay } from "node:timers/promises";
import { developmentCommands, serverEnvironment } from "./run.mjs";

const moduleURL = new URL("./run.mjs", import.meta.url).href;

async function waitFor(check) {
  for (let attempt = 0; attempt < 200; attempt++) {
    if (check()) return;
    await delay(10);
  }
  assert.fail("Child did not reach the expected state");
}

function launch(commands, options = {}) {
  const source = `import { supervise } from ${JSON.stringify(moduleURL)}; process.exitCode = await supervise(${JSON.stringify(commands)}, ${JSON.stringify(options)});`;
  const child = spawn(process.execPath, ["--input-type=module", "-e", source], {
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  child.stdout.on("data", (data) => {
    output += data;
  });
  child.stderr.on("data", (data) => {
    output += data;
  });
  const closed = once(child, "close");
  return { child, closed, output: () => output };
}

function command(name, source) {
  return { name, command: process.execPath, args: ["-e", source] };
}

test("root command includes API and both web servers without shell execution", () => {
  const env = {
    npm_execpath: "/path with spaces/pnpm.cjs",
    AIPROUTER_PUBLIC_URL: "http://127.0.0.1:4173",
  };
  const commands = developmentCommands(env);
  assert.deepEqual(
    commands.map(({ name }) => name),
    ["api", "console", "docs"],
  );
  assert.deepEqual(commands[0].args, ["run", "./servers/aiprouter"]);
  assert.equal(commands[0].env.AIPROUTER_PUBLIC_URL, env.AIPROUTER_PUBLIC_URL);
  assert.deepEqual(commands[1].args, [
    env.npm_execpath,
    "--dir",
    "apps/aiprouter",
    "dev",
  ]);
  assert.equal(commands[1].env, env);
  assert.deepEqual(commands[2].args, [
    env.npm_execpath,
    "--dir",
    "apps/aiprouter-docs",
    "dev",
  ]);
  assert.throws(() => developmentCommands({}), /pnpm dev/);
});

test(
  "a failed child stops siblings and preserves its exit code",
  { timeout: 10000 },
  async (t) => {
    const directory = mkdtempSync(join(tmpdir(), "aiprouter-dev-"));
    t.after(() => rmSync(directory, { recursive: true, force: true }));
    const marker = join(directory, "stopped");
    const worker = `const fs = require('node:fs'); process.on('SIGTERM', () => { fs.writeFileSync(${JSON.stringify(marker)}, 'stopped'); process.exit(0); }); console.log('worker_ready'); setInterval(() => {}, 1000);`;
    const run = launch([
      command("worker", worker),
      command("failure", "setTimeout(() => process.exit(7), 500)"),
    ]);
    t.after(() => run.child.kill("SIGKILL"));
    assert.deepEqual(await run.closed, [7, null]);
    assert.match(run.output(), /worker_ready/);
    if (process.platform !== "win32")
      assert.equal(readFileSync(marker, "utf8"), "stopped");
  },
);

test(
  "a spawn error stops running children and reports failure",
  { timeout: 10000 },
  async (t) => {
    const run = launch([
      command("worker", "setInterval(() => {}, 1000)"),
      {
        name: "missing",
        command: "aiprouter-nonexistent-executable",
        args: [],
      },
    ]);
    t.after(() => run.child.kill("SIGKILL"));
    assert.deepEqual(await run.closed, [1, null]);
    assert.match(run.output(), /dev_start_failed/);
  },
);

test(
  "SIGINT reaches descendants and forces an unresponsive process to exit",
  { skip: process.platform === "win32", timeout: 10000 },
  async (t) => {
    const directory = mkdtempSync(join(tmpdir(), "aiprouter-dev-"));
    t.after(() => rmSync(directory, { recursive: true, force: true }));
    const marker = join(directory, "descendant-stopped");
    const grandchild = `require('node:fs').writeFileSync(${JSON.stringify(join(directory, "ready"))}, 'ready'); process.on('SIGINT', () => { require('node:fs').writeFileSync(${JSON.stringify(marker)}, 'stopped'); process.exit(0); }); setInterval(() => {}, 1000);`;
    const parent = `require('node:child_process').spawn(process.execPath, ['-e', ${JSON.stringify(grandchild)}], {stdio: 'inherit'}); process.on('SIGINT', () => {}); setInterval(() => {}, 1000);`;
    const run = launch([command("tree", parent)], { graceMs: 100 });
    t.after(() => run.child.kill("SIGKILL"));
    await waitFor(() => existsSync(join(directory, "ready")));
    run.child.kill("SIGINT");
    assert.deepEqual(await run.closed, [130, null]);
    assert.equal(readFileSync(marker, "utf8"), "stopped");
  },
);

test(
  "SIGTERM stops the group and returns the signal exit status",
  { skip: process.platform === "win32", timeout: 10000 },
  async (t) => {
    const run = launch([
      command(
        "worker",
        "console.log('worker_ready'); setInterval(() => {}, 1000)",
      ),
    ]);
    t.after(() => run.child.kill("SIGKILL"));
    await waitFor(() => run.output().includes("worker_ready"));
    run.child.kill("SIGTERM");
    assert.deepEqual(await run.closed, [143, null]);
  },
);

test("server-local environment supports quoted JSON and exported overrides", (t) => {
  const directory = mkdtempSync(join(tmpdir(), "aiprouter-dev-env-"));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const path = join(directory, ".env");
  writeFileSync(
    path,
    `AIPROUTER_PUBLIC_URL=http://127.0.0.1:4173
AIPROUTER_ENCRYPTION_KEYS='{"v1":"synthetic"}'
`,
  );
  const exported = { AIPROUTER_PUBLIC_URL: "https://override.example.invalid" };
  assert.deepEqual(serverEnvironment(exported, path), {
    ...exported,
    AIPROUTER_ENCRYPTION_KEYS: '{"v1":"synthetic"}',
  });
  assert.deepEqual(exported, {
    AIPROUTER_PUBLIC_URL: "https://override.example.invalid",
  });
  assert.deepEqual(
    serverEnvironment(exported, join(directory, "missing")),
    exported,
  );
  assert.throws(() => serverEnvironment(exported, directory));
});
