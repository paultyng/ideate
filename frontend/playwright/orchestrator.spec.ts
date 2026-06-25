import { test, expect } from '@playwright/test'
import { enablePtyCapture, readSessionReplay, stopAllRunningSessions, waitForAgentReady, waitForTerminalMount } from './ptyCapture'

// End-to-end coverage for the Phase C orchestrator orchestration tools.
// The /mcp-call slash on the upstream paultyng/testagent claude
// subcommand lets us drive the MCP layer through the same HTTP path
// a real agent would, without writing a separate MCP client harness.
//
// Output shapes the assertions look for:
//   "▶ mcp:ideate.<tool> <args>"  — request line printed by upstream
//                                    once the tool returned 2xx
//   "✗ mcp error: <reason>"        — failure path

test.describe('Orchestrator orchestrator', () => {
  test.afterEach(async ({ page }) => {
    await stopAllRunningSessions(page)
  })

  test('list_sessions returns idea sessions and excludes the orchestrator itself', async ({ page }) => {
    const ideaName = `Orch List ${Date.now()}`

    // Spawn one idea-bound session + the orchestrator. Both run testagent;
    // both register MCP servers (per-idea + root respectively). The
    // orchestration tools live only on the root MCP server, and they
    // intentionally hide the caller's own orchestrator sessions from
    // every introspection path so the orchestrator can't target itself.
    await page.goto('/')
    await enablePtyCapture(page)
    const ideaResult = await page.evaluate(async (name) => {
      // @ts-expect-error wails binding
      const slug = (await window.go.app.App.CreateIdea(name, 'active', '')) as string
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as {
        uuid: string
        uuid: string
      }
      return { slug, ...r }
    }, ideaName)

    const scratch = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      return (await window.go.app.App.StartRootSession('testagent')) as {
        uuid: string
        uuid: string
      }
    })

    // Drawer is auto-pinned on the dashboard (/) so it's already
    // visible — no toggle click needed. Wait for the terminal
    // container to mount, then for upstream's banner before driving
    // the MCP tool. Bubbletea sets stdin to raw mode only after its
    // first render — bytes written before then are eaten by the
    // PTY's line discipline.
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })
    // The drawer's TerminalPanel mounts in response to the
    // idea:changed event published by StartRootSession; wait for the
    // registry entry so capturePty has wired its subscription before
    // we poll for the banner.
    await waitForTerminalMount(page, scratch.uuid, 15000)
    await waitForAgentReady(page, scratch.uuid)
    await page.evaluate(async (uuid) => {
      // @ts-expect-error wails binding
      await window.go.app.App.WriteToSession(uuid, '/mcp-call ideate.list_sessions {}\r')
    }, scratch.uuid)

    // Poll the orchestrator's vscreen for the success line or an mcp
    // error so failures surface a real reason rather than timing out
    // blind. vscreen is a backend snapshot that doesn't depend on
    // capturePty's TerminalPanel subscription timing.
    await expect.poll(async () => {
      const t = await readSessionReplay(page, scratch.uuid)
      if (t.includes('▶ mcp:ideate.list_sessions') && t.includes(ideaResult.uuid)) return 'ok'
      if (t.includes('✗ mcp error')) return 'err'
      return ''
    }, { timeout: 15000 }).toBe('ok')

    const transcript = await readSessionReplay(page, scratch.uuid)
    expect(transcript).toContain('▶ mcp:ideate.list_sessions')
    expect(transcript).toContain(ideaResult.uuid)
    // The orchestrator's own orchestrator must NOT appear in the JSON
    // payload — orchestration tools refuse self-targeting and hide the
    // caller's own sessions. Asserting on the synthetic slug catches
    // any orchestrator entry without false-positiving on the orchestrator
    // testagent's own banner (which includes its own UUID in the
    // session header line).
    expect(transcript).not.toContain('"idea_slug": "__orchestrator__"')
  })

  test('send_session_input wraps text with the orchestrator prefix', async ({ page }) => {
    const ideaName = `Orch Send ${Date.now()}`

    await page.goto('/')
    await enablePtyCapture(page)
    const ideaResult = await page.evaluate(async (name) => {
      // @ts-expect-error wails binding
      const slug = (await window.go.app.App.CreateIdea(name, 'active', '')) as string
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as {
        uuid: string
        uuid: string
      }
      return { slug, ...r }
    }, ideaName)

    const scratch = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      return (await window.go.app.App.StartRootSession('testagent')) as {
        uuid: string
      }
    })

    // Drawer is auto-pinned on /, so it's already visible without
    // toggling. Wait for its terminal then hash-nav to the idea
    // session view so the per-idea TerminalPanel mounts too —
    // both terminals stream into __ideateTerminals, where the
    // orchestrator's input lands in the target buffer.
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })

    // Wait for the orchestrator's raw-mode readiness while the drawer's
    // TerminalPanel is still mounted on the dashboard route — once we
    // hash-nav to the idea-session view the drawer can hide/unmount,
    // and capturePty stops receiving its live bytes. The idea session
    // started ~100ms earlier and reaches raw mode by the time the
    // orchestrator does, but its own banner is not assertable here
    // because Replay is a vscreen snapshot (current screen state, not
    // a byte log) and its TerminalPanel mounts late on this route.
    await waitForAgentReady(page, scratch.uuid)

    await page.evaluate((u) => {
      window.location.hash = u
    }, `/idea/${ideaResult.slug}/session/${ideaResult.uuid}`)
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    // TerminalPanel's useEffect wires capturePty in the same pass
    // that registers the terminal — wait for the registry entry so
    // we don't race the MCP-mediated bytes against an unsubscribed
    // capture.
    await waitForTerminalMount(page, ideaResult.uuid)

    // Send the orchestrator input — submit defaults to true so testagent
    // submits the buffered turn and echoes back.
    const args = JSON.stringify({
      uuid: ideaResult.uuid,
      text: 'orch hello',
    })
    await page.evaluate(
      async ([uuid, payload]) => {
        // @ts-expect-error wails binding
        await window.go.app.App.WriteToSession(uuid, `/mcp-call ideate.send_session_input ${payload}\r`)
      },
      [scratch.uuid, args] as const,
    )

    // Target session's vscreen gets the wrapped prefix + payload.
    // Surface any ✗ mcp error from the orchestrator side first —
    // that's where MCP errors render. Reading vscreen via
    // GetSessionReplay avoids capturePty's subscription-window race.
    await expect.poll(async () => {
      const target = await readSessionReplay(page, ideaResult.uuid)
      if (target.includes('[Input from Orchestrating Agent') && target.includes('orch hello')) return 'ok'
      const orch = await readSessionReplay(page, scratch.uuid)
      if (orch.includes('✗ mcp error')) return 'err'
      return ''
    }, { timeout: 15000 }).toBe('ok')
  })

  // Full round-trip: orchestrator delegates a turn via send_session_input
  // with include_reply_hint=true (interactive-orchestration opt-in), then
  // the receiving idea agent calls reply_to_orchestrator, and the reply
  // lands in the orchestrator terminal wrapped with `[Reply from <idea name>]`.
  // Exercises the per-idea reply_to_orchestrator MCP tool plus the wire
  // format on both sides. include_reply_hint=true is REQUIRED — the gate
  // introduced when reply was made opt-in refuses reply_to_orchestrator
  // when the most recent send was fire-and-forget (the default).
  test('reply_to_orchestrator routes a reply back to the orchestrator', async ({ page }) => {
    const ideaName = `Orch Reply ${Date.now()}`

    await page.goto('/')
    await enablePtyCapture(page)
    const ideaResult = await page.evaluate(async (name) => {
      // @ts-expect-error wails binding
      const slug = (await window.go.app.App.CreateIdea(name, 'active', '')) as string
      // @ts-expect-error wails binding
      const r = (await window.go.app.App.StartIdeaSession(slug, 'testagent', false)) as {
        uuid: string
        uuid: string
      }
      return { slug, ...r }
    }, ideaName)

    const scratch = await page.evaluate(async () => {
      // @ts-expect-error wails binding
      return (await window.go.app.App.StartRootSession('testagent')) as {
        uuid: string
      }
    })

    // Mount both terminals — the per-idea TerminalPanel is what
    // surfaces the reply tool's MCP server endpoint registration.
    await page.waitForSelector('.orchestrator-host .terminal-container', { timeout: 10000 })

    // The drawer's TerminalPanel may take a beat to rebind after
    // page.goto when the orchestrator was started via direct binding
    // (instead of through the drawer's start callback). Wait for the
    // registry entry first so capturePty is wired before we poll for
    // the banner — otherwise the banner bytes can land before
    // subscription and waitForAgentReady times out.
    await waitForTerminalMount(page, scratch.uuid, 15000)

    // Wait for orchestrator readiness BEFORE hash-nav — see the
    // send_session_input test for the unmount caveat.
    await waitForAgentReady(page, scratch.uuid)

    await page.evaluate((u) => {
      window.location.hash = u
    }, `/idea/${ideaResult.slug}/session/${ideaResult.uuid}`)
    await page.waitForSelector('.terminal-container', { timeout: 10000 })
    // TerminalPanel's useEffect wires capturePty in the same pass
    // that registers the terminal — wait for the registry entry so
    // we don't race the MCP-mediated bytes against an unsubscribed
    // capture.
    await waitForTerminalMount(page, ideaResult.uuid)

    // Step 1 — orchestrator delegates a turn to the idea session.
    // include_reply_hint=true opens the reply gate so step 2's
    // reply_to_orchestrator lands instead of being refused as
    // fire-and-forget.
    const sendArgs = JSON.stringify({
      uuid: ideaResult.uuid,
      text: 'check the index strategy',
      include_reply_hint: true,
    })
    await page.evaluate(
      async ([uuid, payload]) => {
        // @ts-expect-error wails binding
        await window.go.app.App.WriteToSession(uuid, `/mcp-call ideate.send_session_input ${payload}\r`)
      },
      [scratch.uuid, sendArgs] as const,
    )

    // Wait until the idea-side vscreen shows the input prefix —
    // proves the delegation reached the receiver.
    await expect.poll(async () => {
      const t = await readSessionReplay(page, ideaResult.uuid)
      return t.includes('[Input from Orchestrating Agent') && t.includes('check the index strategy')
    }, { timeout: 15000 }).toBe(true)

    // Step 2 — the idea agent replies via reply_to_orchestrator.
    // testagent's /mcp command piggybacks on its per-idea MCP server,
    // which now carries the reply tool registered in addTools.
    const replyArgs = JSON.stringify({ text: 'leaning incremental WAL' })
    await page.evaluate(
      async ([uuid, payload]) => {
        // @ts-expect-error wails binding
        await window.go.app.App.WriteToSession(uuid, `/mcp-call ideate.reply_to_orchestrator ${payload}\r`)
      },
      [ideaResult.uuid, replyArgs] as const,
    )

    // Step 3 — assert the reply lands in the orchestrator terminal with
    // the "[Reply from <idea name>]" prefix. Read the orchestrator's
    // PTY through GetSessionReplay rather than __capturedPty because the
    // orchestrator drawer's terminal isn't mounted on the
    // /idea/<slug>/session/<uuid> route, so its PTY events don't flow
    // through TerminalPanel's capture hook on this page.
    // GetSessionReplay reads the backend's per-session ring buffer so
    // it's independent of which view is currently rendering.
    //
    // Re-resolve the orchestrator coordID via GetRunningRootSession on
    // each poll: the testagent inactivity-exit can recycle the
    // orchestrator between StartRootSession and the moment the reply
    // lands.
    await expect.poll(async () => {
      const orch = await page.evaluate(async () => {
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        const W = window as any
        try {
          const cur = (await W.go.app.App.GetRunningRootSession()) as { uuid: string }
          if (!cur?.uuid) return ''
          const b64 = (await W.go.app.App.GetSessionReplay(cur.uuid)) as string
          return b64 ? atob(b64) : ''
        } catch {
          return ''
        }
      })
      if (orch.includes(`[Reply from ${ideaName}]`) && orch.includes('leaning incremental WAL')) return 'ok'
      const idea = await readSessionReplay(page, ideaResult.uuid)
      if (idea.includes('✗ mcp error')) return 'err'
      return ''
    }, { timeout: 15000 }).toBe('ok')
  })
})
