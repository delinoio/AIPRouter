// SPDX-License-Identifier: Apache-2.0
import { spawn } from "node:child_process";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { parseEnv } from "node:util";

export const root = fileURLToPath(new URL("../../", import.meta.url));

export function serverEnvironment(
  env = process.env,
  envFile = resolve(root, "servers/aiprouter/.env"),
) {
  let local = {};
  try {
    local = parseEnv(readFileSync(envFile, "utf8"));
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }
  return { ...local, ...env };
}

// Separate process groups let shutdown reach go run's binary and pnpm's children.
// Windows has no POSIX process groups, so taskkill is the required tree shim.
export async function supervise(commands, { graceMs = 35_000 } = {}) {
  const children = [];
  let stopping = false;
  let finish;
  const stopped = new Promise((resolve) => {
    finish = resolve;
  });
  function kill(child, signal) {
    if (!child.pid) return;
    if (process.platform === "win32") {
      if (child.exitCode !== null || child.signalCode !== null) return;
      const taskkill = spawn(
        "taskkill",
        ["/pid", String(child.pid), "/T", "/F"],
        { shell: false, stdio: "ignore" },
      );
      taskkill.on("error", () => child.kill());
    } else {
      try {
        process.kill(-child.pid, signal);
      } catch (error) {
        if (error.code !== "ESRCH") throw error;
      }
    }
  }
  async function stop(code, signal = "SIGTERM") {
    if (stopping) return;
    stopping = true;
    console.error(JSON.stringify({ event: "dev_stopping", signal, code }));
    const timer = setTimeout(() => {
      for (const { child } of children) kill(child, "SIGKILL");
    }, graceMs);
    for (const { child } of children) kill(child, signal);
    await Promise.all(children.map(({ closed }) => closed));
    // A wrapper can exit before its descendants finish; remove any survivors.
    if (process.platform !== "win32") {
      for (const { child } of children) kill(child, "SIGKILL");
    }
    clearTimeout(timer);
    finish(code);
  }
  const interrupt = () => {
    void stop(130, "SIGINT");
  };
  const terminate = () => {
    void stop(143);
  };
  process.on("SIGINT", interrupt);
  process.on("SIGTERM", terminate);
  try {
    for (const { name, command, args, env } of commands) {
      if (stopping) break;
      console.error(JSON.stringify({ event: "dev_starting", name }));
      const child = spawn(command, args, {
        cwd: root,
        env,
        shell: false,
        stdio: "inherit",
        detached: process.platform !== "win32",
      });
      const closed = new Promise((resolve) => child.once("close", resolve));
      children.push({ child, closed });
      child.once("error", (error) => {
        console.error(
          JSON.stringify({ event: "dev_start_failed", name, code: error.code }),
        );
        void stop(1);
      });
      child.once("exit", (code, signal) => {
        console.error(
          JSON.stringify({ event: "dev_exited", name, code, signal }),
        );
        void stop(code || 1);
      });
    }
    if (!commands.length) void stop(0);
    return await stopped;
  } finally {
    process.off("SIGINT", interrupt);
    process.off("SIGTERM", terminate);
  }
}

export function developmentCommands(env = process.env) {
  if (!env.npm_execpath) throw new Error("Run this launcher with pnpm dev.");
  const frontend = (name, directory) => ({
    name,
    command: process.execPath,
    args: [env.npm_execpath, "--dir", directory, "dev"],
    env,
  });
  return [
    {
      name: "api",
      command: "go",
      args: ["run", "./servers/aiprouter"],
      env: serverEnvironment(env),
    },
    frontend("console", "apps/aiprouter"),
    frontend("docs", "apps/aiprouter-docs"),
  ];
}

if (
  process.argv[1] &&
  resolve(process.argv[1]) === fileURLToPath(import.meta.url)
) {
  try {
    process.exitCode = await supervise(developmentCommands());
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}
