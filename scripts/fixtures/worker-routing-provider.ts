// Local deterministic provider for TestWorkerRoutingWithPi. The worker called
// slot sees a narrower live catalog than the catalog shown to Settings/Lead.
import { appendFileSync, existsSync } from 'node:fs';
import { createAssistantMessageEventStream } from '@earendil-works/pi-ai';

export default function (pi) {
  const agent = process.env.YESMAN_AGENT_NAME;
  const defs = ['lead', 'interactive', 'route/build:a', 'fallback'];
  pi.registerProvider('routing-fixture', {
    baseUrl: 'http://127.0.0.1:1', apiKey: 'fixture', api: 'routing-fixture-api',
    models: defs.map((id) => ({ id, name: id, reasoning: true,
      thinkingLevelMap: id === 'route/build:a' && agent === 'slot' ? { high: null, low: null } : {},
      input: ['text'], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 100000, maxTokens: 1000 })),
    streamSimple(model, context, options) {
      const stream = createAssistantMessageEventStream();
      const last = context.messages.at(-1);
      const text = typeof last?.content === 'string' ? last.content : (last?.content ?? []).map((c) => c.text || '').join('');
      const command = agent === 'lead' && last?.role === 'user' && text.startsWith('START ') ? JSON.parse(text.slice(6)) : null;
      // Pi's provider input is a transcript. Prompt sections and tool updates
      // live on its system messages, including changes after the first turn.
      let instructions = '';
      const tools = new Map();
      for (const msg of context.messages.filter((m) => m.role === 'system')) {
        if (msg.sections && 'yesman' in msg.sections) instructions = msg.sections.yesman || '';
        for (const t of msg.toolsAdded || []) tools.set(t.name, t);
        for (const t of msg.toolsRemoved || []) tools.delete(t.name);
      }
      const route = instructions.split('## Crew\n')[1]?.split('## ')[0] ?? '';
      if (command?.name === 'matched' || command?.name === 'catalog-down') {
        // A deterministic lead can pick from the real score provenance it saw.
        command.crew = route.split('\n').find((l) => l.includes('coding 89% (user CSV)'))?.match(/^- "([^"]+)"/)?.[1];
      }
      const tool = tools.get('yesman_worker_start');
      appendFileSync(process.env.ROUTING_AUDIT, JSON.stringify({ agent, model: model.id, provider: model.provider, effort: options?.reasoning || 'off',
        text: text.slice(0, 120), route, hasTool: !!tool, hasLabel: !!tool?.parameters?.properties?.label }) + '\n');
      // Hold the lead's pre-dispatch turn. The Go test edits its pool while
      // this process still sees the old scores/candidates.
      (async () => {
        while (command?.name === 'stale' && !existsSync(process.env.ROUTING_GATE) && !options?.signal?.aborted) {
          await new Promise((resolve) => setTimeout(resolve, 10));
        }
        const content = command ? [{ type: 'toolCall', id: `start-${command.name}`, name: 'yesman_worker_start', arguments: { title: 'Fixture work', task: `Deliver ${command.name}`, ...command } }]
          : [{ type: 'text', text: 'Fixture complete.' }];
        const output = { role: 'assistant', api: model.api, provider: model.provider, model: model.id, timestamp: Date.now(), content,
          usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } }, stopReason: command ? 'toolUse' : 'stop' };
        stream.push({ type: 'start', partial: output });
        stream.push({ type: 'done', reason: output.stopReason, message: output });
        stream.end();
      })();
      return stream;
    },
  });
}
