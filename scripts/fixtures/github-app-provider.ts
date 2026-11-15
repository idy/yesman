// Synthetic provider for the real pi/production shell integration test.
import { appendFileSync } from 'node:fs';
import { createAssistantMessageEventStream } from '@earendil-works/pi-ai';
export default function (pi) {
  pi.registerProvider('github-app-fixture', {
    baseUrl: 'http://127.0.0.1:1', apiKey: 'fixture', api: 'github-app-fixture-api',
    models: [{ id: 'm', name: 'Fixture', reasoning: false, input: ['text'], contextWindow: 100000, maxTokens: 1000, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } }],
    streamSimple(model, context) {
      appendFileSync(process.env.GITHUB_APP_AUDIT, JSON.stringify(context) + '\n');
      const called = context.messages.some((m) => m.role === 'toolResult');
      const output = {
        role: 'assistant', api: model.api, provider: model.provider, model: model.id,
        content: called ? [{ type: 'text', text: 'fixture complete' }] : [{ type: 'toolCall', id: 'app-pr-fixture', name: 'bash', arguments: { command: 'gh pr create --repo idy/yesman --head test/app-fixture --base main --title "test: App fixture" --body "Synthetic test body" --draft' } }],
        usage: { input: 1, output: 1, cacheRead: 0, cacheWrite: 0, totalTokens: 2, cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 } },
        stopReason: called ? 'stop' : 'toolUse', timestamp: Date.now(),
      };
      const stream = createAssistantMessageEventStream();
      queueMicrotask(() => { stream.push({ type: 'start', partial: output }); stream.push({ type: 'done', reason: output.stopReason, message: output }); stream.end(); });
      return stream;
    },
  });
}
