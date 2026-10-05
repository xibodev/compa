import { describe, expect, it } from "vitest"

import "@/i18n"

import {
  EMPTY_FORM,
  buildFormFromConfig,
  unmaskSecretValues,
} from "./form-model"

describe("buildFormFromConfig", () => {
  it("reads the command and logging settings", () => {
    const form = buildFormFromConfig({
      commands: { owner_only: false },
      logging: { redact_secrets: false, max_size_mb: 25, max_files: 2 },
    })
    expect(form.commandsOwnerOnly).toBe(false)
    expect(form.logRedactSecrets).toBe(false)
    expect(form.logMaxSizeMB).toBe("25")
    expect(form.logMaxFiles).toBe("2")
  })

  it("shows the defaults the gateway applies when they are unset", () => {
    const form = buildFormFromConfig({
      logging: { max_size_mb: 0, max_files: 0 },
    })
    expect(form.commandsOwnerOnly).toBe(EMPTY_FORM.commandsOwnerOnly)
    expect(form.logRedactSecrets).toBe(true)
    expect(form.logMaxSizeMB).toBe("10")
    expect(form.logMaxFiles).toBe("5")
    // No approval policy allows every call.
    expect(form.approvalDefault).toBe("allow")
    expect(form.approvalRules).toEqual([])
  })

  it("reads the approval rules in order, keeping those it cannot show as they came", () => {
    const unknownOrigin = { tool: "exec", origin: ["email"], action: "ask" }
    const unknownAction = { tool: "install_skill", action: "escalate" }
    const unknownField = { tool: "web_fetch", note: "x", action: "deny" }
    const form = buildFormFromConfig({
      tools: {
        approval: {
          default: "deny",
          rules: [
            {
              source: "module:*",
              hints: ["cost_unknown", "network"],
              action: "ask",
            },
            unknownOrigin,
            unknownAction,
            unknownField,
            { tool: "spawn", origin: ["cron"], action: "hide" },
          ],
        },
      },
    })

    expect(form.approvalDefault).toBe("deny")
    expect(form.approvalRules.map((rule) => rule.kept)).toEqual([
      undefined,
      unknownOrigin,
      unknownAction,
      unknownField,
      undefined,
    ])
    expect(form.approvalRules[0]).toMatchObject({
      tool: "",
      source: "module:*",
      origin: [],
      hints: ["cost_unknown", "network"],
      action: "ask",
    })
    expect(form.approvalRules[4]).toMatchObject({
      tool: "spawn",
      origin: ["cron"],
      action: "hide",
    })
    // A kept rule still shows what it holds.
    expect(form.approvalRules[1]).toMatchObject({
      tool: "exec",
      origin: ["email"],
      action: "ask",
    })
  })

  it("shows stored MCP secrets as set but hidden, and keeps them on save", () => {
    const form = buildFormFromConfig({
      tools: {
        mcp: {
          servers: {
            github: {
              command: "gh-mcp",
              env: { TOKEN: "[NOT_HERE]", MODE: "" },
            },
            remote: {
              type: "http",
              url: "https://mcp.example.test",
              headers: { Authorization: "[NOT_HERE]" },
            },
          },
        },
      },
    })
    const [github, remote] = form.mcpServers
    expect(github.envText).not.toContain("[NOT_HERE]")
    expect(JSON.parse(github.envText)).toEqual({ TOKEN: "********", MODE: "" })
    expect(JSON.parse(remote.headersText)).toEqual({
      Authorization: "********",
    })

    // Untouched values go back as the placeholder; typed ones as typed.
    expect(
      unmaskSecretValues({ TOKEN: "********", MODE: "fast", NEW: "secret" }),
    ).toEqual({ TOKEN: "[NOT_HERE]", MODE: "fast", NEW: "secret" })
  })
})
