/**
 * TypeSafe gate for omp.
 *
 * Mirrors the Claude Code PreToolUse hook: before a side-effecting tool runs,
 * it asks the same Go binary for a verdict. A deny blocks the call outright.
 * An ask becomes a real confirmation dialog when omp has a UI, so the judgment
 * reaches the person who can act on it rather than the model.
 *
 * Install:
 *   omp --hook /path/to/typesafe-gate.hook.ts
 * or add the path to hooks in ~/.omp/agent/config.yml.
 *
 * Environment:
 *   TYPESAFE_GATE_BIN            path to the gate binary (default: alongside this file)
 *   TYPESAFE_GATE=off            disable without unloading the hook
 *   TYPESAFE_GATE_HEADLESS=block treat "ask" as a block when there is no UI
 */

import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

type Verdict = { block?: boolean; reason?: string; note?: string };

const HERE = dirname(fileURLToPath(import.meta.url));
const BIN = process.env.TYPESAFE_GATE_BIN || join(HERE, "..", "bin", "typesafe-gate");

/** Tools that cannot change anything, so they never cost a round trip. */
const READ_ONLY = new Set([
  "read", "grep", "glob", "ls", "list", "todo", "websearch", "fetch", "think",
]);

export default function (pi: any) {
  // The user's own words, kept so the gate can judge whether a call serves them.
  let userRequest = "";

  pi.on("before_agent_start", (event: { prompt?: string }) => {
    if (event?.prompt) userRequest = event.prompt.slice(0, 2000);
  });

  pi.on("tool_call", async (event: any, ctx: any): Promise<Verdict | undefined> => {
    if (process.env.TYPESAFE_GATE === "off") return;
    const tool = String(event?.toolName ?? "");
    if (!tool || READ_ONLY.has(tool.toLowerCase())) return;

    const payload = JSON.stringify({
      toolName: tool,
      toolCallId: event.toolCallId ?? "",
      input: event.input ?? {},
      cwd: ctx?.cwd ?? process.cwd(),
      userRequest,
      sessionId: ctx?.sessionManager?.getSessionId?.() ?? "",
      model: ctx?.model?.id ?? ctx?.model?.name ?? "",
    });

    let verdict: Verdict;
    try {
      const res = await pi.exec(BIN, ["-mode=omp", "-timeout=6s", `-payload=${payload}`], {
        timeout: 8000,
      });
      if (res.code !== 0 || !res.stdout.trim()) return; // fail open
      verdict = JSON.parse(res.stdout) as Verdict;
    } catch (err) {
      // A gate that breaks the agent loop is worse than no gate.
      pi.logger?.warn?.(`typesafe-gate: ${String(err)}`);
      return;
    }

    if (verdict.block) {
      ctx?.ui?.notify?.(`Blocked by TypeSafe gate: ${verdict.reason}`, "error");
      return { block: true, reason: verdict.reason ?? "Blocked by the TypeSafe gate." };
    }

    if (verdict.note) {
      // omp has no "ask" in its result type, so turn it into a real question.
      if (ctx?.hasUI && ctx.ui?.confirm) {
        const ok = await ctx.ui.confirm("TypeSafe gate", `${verdict.note}\n\nRun it anyway?`);
        if (!ok) {
          return { block: true, reason: `Declined by the user after a gate warning: ${verdict.note}` };
        }
        return;
      }
      if (process.env.TYPESAFE_GATE_HEADLESS === "block") {
        return { block: true, reason: verdict.note };
      }
      ctx?.ui?.notify?.(verdict.note, "warning");
    }
    return;
  });

  pi.registerCommand?.("gate", {
    description: "Check how the TypeSafe gate would judge a shell command",
    handler: async (args: string, ctx: any) => {
      if (!args.trim()) {
        ctx.ui.notify("usage: /gate <shell command>", "info");
        return;
      }
      const payload = JSON.stringify({
        toolName: "bash",
        input: { command: args },
        cwd: ctx.cwd,
        userRequest,
      });
      const res = await pi.exec(BIN, ["-mode=omp", `-payload=${payload}`], { timeout: 8000 });
      const v = JSON.parse(res.stdout || "{}") as Verdict;
      const verdict = v.block ? `deny: ${v.reason}` : v.note ? `ask: ${v.note}` : "allow";
      ctx.ui.notify(verdict, v.block ? "error" : v.note ? "warning" : "info");
    },
  });
}
