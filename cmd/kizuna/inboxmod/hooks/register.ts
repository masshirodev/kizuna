import type { Register } from 'claude-code'

// How often the session tells kizuna it is alive and, when idle, collects
// its mail. `kizuna send` reads a heartbeat older than 15 s as a dead mod and
// falls back to typing, so this must stay well under that.
const POLL_MS = 2000

type Poll = { unread: number; text?: string }

export const register: Register = on => {
  let isBusy = false
  // One poll at a time: a tick that lands while the last one is still
  // draining or submitting would otherwise race it.
  let isPolling = false

  on('turn.start', ($, e, next) => {
    if (e.agentId === undefined) isBusy = true
    return next(e)
  })

  on('turn.complete', ($, e, next) => {
    if (e.agentId === undefined) isBusy = false
    return next(e)
  })

  on('session.start', async ($, e, next) => {
    const started = await next(e)

    const timer = $.clock.every(POLL_MS, async () => {
      if (isPolling) return
      isPolling = true
      try {
        // While a turn runs the hooks deliver at the next tool call, so a
        // busy session only refreshes its heartbeat.
        const argv = isBusy ? ['kizuna', 'inbox', '--mod'] : ['kizuna', 'inbox', '--mod', '--drain']
        const run = await $.process.run(argv, { timeoutMs: 10_000 })
        if (run.exitCode !== 0) {
          // Not a kizuna-launched session, or a kizuna too old for --mod:
          // nothing here will ever answer, so stop asking every two seconds.
          timer.cancel()
          $.ui.log(`kizuna-inbox: stopped, kizuna inbox --mod failed: ${run.stderr.trim()}`, { to: 'debug' })
          return
        }
        const poll = JSON.parse(run.stdout) as Poll
        if (!poll.text) return

        // Submitting never touches the prompt box, so a half-typed draft
        // stays exactly as the person left it; the toast says why a turn
        // started under it.
        const { text: draft } = await $.prompt.read()
        if (draft.trim() !== '') $.ui.toast('kizuna: another agent sent a message; your draft is untouched')

        // Resolves once the turn has started, so turn.start has marked the
        // session busy before the next tick can look.
        await $.prompt.submit({ text: poll.text })
      } catch (error) {
        $.ui.log(`kizuna-inbox: ${String(error)}`, { to: 'debug' })
      } finally {
        isPolling = false
      }
    })

    return started
  })
}
