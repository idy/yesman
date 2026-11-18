import test from 'node:test';
import assert from 'node:assert/strict';
import { recordsByDay, validDay } from '../web/task-log.js';

const hash = 'a'.repeat(64);
const file = (name) => ({ name, path: `.yesman/task-log/${name}`, dir: false });

test('only real calendar days are dates', () => {
  assert.equal(validDay('2026-02-28'), '2026-02-28');
  assert.equal(validDay('2028-02-29'), '2028-02-29');
  for (const bad of ['2026-02-29', '2026-13-01', '2026-00-10', '2026-1-01', '1969-12-31', '', null, '2026-01-15x']) assert.equal(validDay(bad), null, String(bad));
});

test('records group by day, newest first, keeping the TOC order within a day', () => {
  const toc = [
    '# Working log',
    '',
    '<!-- yesman-task-log-toc v1 -->',
    '- 2026-01-15 · t3 · [Fix picker](task-log/2026-01-15-t3-picker.md) — released.',
    '- 2026-01-16 · t9 · [Release 0.1.9](task-log/2026-01-16-release-019.md#outcome) — published.',
    // The same day again, after another: the TOC order holds, not names.
    '- 2026-01-15 · t10 · [A later PR](task-log/2026-01-15-t10-pr.md) — merged.',
    '- 2026-01-15 · t2 · [Earlier task](task-log/2026-01-15-t2-early.md)',
    // The line's date wins over a name without one.
    '- 2026-01-16 · [Custom](task-log/notes-custom.md)',
    // A repeated link is one record.
    '- 2026-01-15 · [Fix picker again](task-log/2026-01-15-t3-picker.md#validation)',
    // Neither the TOC nor the file workspace: not records.
    '[Knowledge](knowledge/INDEX.md) [Site](https://example.com) [Up](../task-log.md) [Escape](../../x.md)',
  ].join('\n');
  const { days, undated } = recordsByDay(toc, []);
  assert.deepEqual(days.map((d) => d.day), ['2026-01-16', '2026-01-15']);
  assert.deepEqual(days[0].records.map((r) => [r.title, r.anchor]), [['Release 0.1.9', 'outcome'], ['Custom', '']]);
  assert.deepEqual(days[1].records.map((r) => r.path.split('/').pop()), ['2026-01-15-t3-picker.md', '2026-01-15-t10-pr.md', '2026-01-15-t2-early.md']);
  assert.equal(undated.length, 0);
});

test('unlinked flat files join their day or the undated group, never by file time', () => {
  const toc = '- 2026-03-01 · [Linked](task-log/2026-03-01-t5-linked.md)\n- [No date in the line](task-log/legacy-' + hash + '-index-0001.md)\n';
  const entries = [
    // Listed out of order, with times that would put them elsewhere.
    { ...file('2026-03-01-t6-unlinked.md'), modTime: '2020-01-01T00:00:00Z' },
    file('2026-03-01-t5-linked.md'),
    file(`legacy-${hash}-original.md`),
    file(`legacy-${hash}-0001.md`),
    file('2026-02-30-t7-impossible.md'),
    file('readme.txt'),
    { name: '资料', path: '.yesman/task-log/资料', dir: true },
  ];
  const { days, undated } = recordsByDay(toc, entries);
  assert.deepEqual(days.map((d) => [d.day, d.records.map((r) => [r.path.split('/').pop(), r.linked])]), [
    ['2026-03-01', [['2026-03-01-t5-linked.md', true], ['2026-03-01-t6-unlinked.md', false]]],
  ]);
  assert.deepEqual(undated.map((r) => r.path.split('/').pop()), [
    `legacy-${hash}-index-0001.md`, '2026-02-30-t7-impossible.md', `legacy-${hash}-0001.md`, `legacy-${hash}-original.md`,
  ]);
});

test('a missing TOC or listing still yields what the other has', () => {
  assert.deepEqual(recordsByDay(null, null), { days: [], undated: [] });
  const { days } = recordsByDay(null, [file('2026-04-02-t8-x.md')]);
  assert.deepEqual(days.map((d) => d.day), ['2026-04-02']);
  const nested = recordsByDay('- 2026-04-03 · [Nested](task-log/%E8%B5%84%E6%96%99/%E6%97%A7%20%E8%AE%B0%E5%BD%95.md)', null);
  assert.equal(nested.days[0].records[0].path, '.yesman/task-log/资料/旧 记录.md');
});
